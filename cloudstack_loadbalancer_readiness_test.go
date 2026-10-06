/*
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

package cloudstack

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBackendReadinessSetting(t *testing.T) {
	for _, tc := range []struct {
		value          string
		set, want, bad bool
	}{
		{want: true}, {value: "true", set: true, want: true}, {value: "false", set: true}, {value: "maybe", set: true, bad: true},
	} {
		svc := &corev1.Service{}
		if tc.set {
			svc.Annotations = map[string]string{ServiceAnnotationLoadBalancerBackendReadiness: tc.value}
		}
		got, err := backendReadinessEnabled(svc)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("option %q: enabled=%v, error=%v", tc.value, got, err)
		}
	}
	// An invalid option is rejected before the cloud client can be accessed.
	bad := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ServiceAnnotationLoadBalancerBackendReadiness: "maybe"}}}
	cs := &CSCloud{}
	if _, err := cs.EnsureLoadBalancer(context.Background(), "test", bad, nil); err == nil {
		t.Fatal("Ensure accepted invalid option")
	}
	if err := cs.UpdateLoadBalancer(context.Background(), "test", bad, nil); err == nil {
		t.Fatal("Update accepted invalid option")
	}
	cfg := &CSConfig{}
	cfg.Global.APIURL = "http://127.0.0.1/unused"
	cfg.Global.APIKey = "test-key"
	cfg.Global.SecretKey = "test-secret"
	cfg.Global.Version = "4.23.0.0"
	provider, err := newCSCloud(cfg)
	if err != nil || provider.backendProbe == nil {
		t.Fatalf("production provider must enable TCP probe: %v", err)
	}
}

func TestBackendTCPReadiness(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := probeTCPBackend(context.Background(), address); err != nil {
		t.Fatalf("open TCP port: %v", err)
	}
	listener.Close()
	if err := probeTCPBackend(context.Background(), address); err == nil {
		t.Fatal("closed TCP port was considered ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probeTCPBackend(ctx, address); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestBackendReadinessOnlyNewAddressesAndBoundedContext(t *testing.T) {
	rule := &cloudstack.LoadBalancerRule{Id: "rule", Protocol: "tcp", Privateport: "30208"}
	var called []string
	probe := func(ctx context.Context, address string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("readiness has no bounded deadline")
		}
		called = append(called, address)
		return nil
	}
	// Neither the existing backend nor an annotation-supplied address is probed.
	if err := checkNewBackendPorts(context.Background(), rule, []string{"new"}, map[string]string{"new": "10.123.6.142", "old": "10.123.6.181"}, probe); err != nil {
		t.Fatal(err)
	}
	if len(called) != 1 || called[0] != "10.123.6.142:30208" {
		t.Fatalf("probed wrong targets: %v", called)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err := checkNewBackendPorts(ctx, rule, []string{"new"}, map[string]string{"new": "10.123.6.142"}, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(begin) > time.Second {
		t.Fatalf("unbounded/cancellation error: %v", err)
	}
	for _, bad := range []string{"", "not-an-ip"} {
		if err := checkNewBackendPorts(context.Background(), rule, []string{"new"}, map[string]string{"new": bad}, probe); err == nil {
			t.Fatal("invalid VM NIC address accepted")
		}
	}
	for _, port := range []string{"0", "65536", "garbage"} {
		invalid := *rule
		invalid.Privateport = port
		if err := checkNewBackendPorts(context.Background(), &invalid, []string{"new"}, map[string]string{"new": "10.0.0.1"}, probe); err == nil {
			t.Fatal("invalid NodePort accepted")
		}
	}
	udp := *rule
	udp.Protocol = "udp"
	if err := checkNewBackendPorts(context.Background(), &udp, []string{"new"}, nil, func(context.Context, string) error { t.Fatal("UDP must not use TCP probe"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestBackendReadinessFailureDoesNotAssignAndRetryDoes(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockLB := cloudstack.NewMockLoadBalancerServiceIface(ctrl)
	unready := errors.New("TCP port is not ready")
	ready := false
	lb := &loadBalancer{CloudStackClient: &cloudstack.CloudStackClient{LoadBalancer: mockLB}}
	lb.beforeAssign = func(rule *cloudstack.LoadBalancerRule, ids []string) error {
		return checkNewBackendPorts(context.Background(), rule, ids, map[string]string{"new": "10.123.6.142"}, func(context.Context, string) error {
			if !ready {
				return unready
			}
			return nil
		})
	}
	rule := &cloudstack.LoadBalancerRule{Id: "rule", Protocol: "tcp", Privateport: "30208"}
	// No SDK expectations are registered until the probe succeeds.
	if err := lb.assignHostsToRule(rule, []string{"new"}); !errors.Is(err, unready) {
		t.Fatalf("not-ready error lost: %v", err)
	}
	ready = true
	mockLB.EXPECT().NewAssignToLoadBalancerRuleParams("rule").Return(&cloudstack.AssignToLoadBalancerRuleParams{})
	mockLB.EXPECT().AssignToLoadBalancerRule(gomock.Any()).DoAndReturn(func(p *cloudstack.AssignToLoadBalancerRuleParams) (*cloudstack.AssignToLoadBalancerRuleResponse, error) {
		ids, _ := p.GetVirtualmachineids()
		if len(ids) != 1 || ids[0] != "new" {
			t.Fatalf("wrong assignment: %v", ids)
		}
		return &cloudstack.AssignToLoadBalancerRuleResponse{}, nil
	})
	if err := lb.assignHostsToRule(rule, []string{"new"}); err != nil {
		t.Fatalf("ready retry failed: %v", err)
	}
}

func TestBackendRemovalContinuesWhileNewNodeIsNotReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	vm := cloudstack.NewMockVirtualMachineServiceIface(ctrl)
	l := cloudstack.NewMockLoadBalancerServiceIface(ctrl)
	rule := &cloudstack.LoadBalancerRule{Id: "rule", Name: "rule", Publicip: "1.2.3.4", Publicipid: "ip", Protocol: "tcp", Privateport: "30208"}
	l.EXPECT().NewListLoadBalancerRulesParams().Return(&cloudstack.ListLoadBalancerRulesParams{})
	l.EXPECT().ListLoadBalancerRules(gomock.Any()).Return(&cloudstack.ListLoadBalancerRulesResponse{Count: 1, LoadBalancerRules: []*cloudstack.LoadBalancerRule{rule}}, nil)
	vm.EXPECT().NewListVirtualMachinesParams().Return(&cloudstack.ListVirtualMachinesParams{})
	vm.EXPECT().ListVirtualMachines(gomock.Any()).Return(&cloudstack.ListVirtualMachinesResponse{Count: 2, VirtualMachines: []*cloudstack.VirtualMachine{
		{Id: "keep", Name: "node-keep", Nic: []cloudstack.Nic{{Networkid: "net", Ipaddress: "10.0.0.1"}}},
		{Id: "new", Name: "node-new", Nic: []cloudstack.Nic{{Networkid: "net", Ipaddress: "10.0.0.3"}}},
	}}, nil)
	l.EXPECT().NewListLoadBalancerRuleInstancesParams("rule").Return(&cloudstack.ListLoadBalancerRuleInstancesParams{})
	l.EXPECT().ListLoadBalancerRuleInstances(gomock.Any()).Return(&cloudstack.ListLoadBalancerRuleInstancesResponse{Count: 2, LoadBalancerRuleInstances: []*cloudstack.VirtualMachine{{Id: "keep"}, {Id: "old"}}}, nil)
	removed := false
	l.EXPECT().NewRemoveFromLoadBalancerRuleParams("rule").Return(&cloudstack.RemoveFromLoadBalancerRuleParams{})
	l.EXPECT().RemoveFromLoadBalancerRule(gomock.Any()).DoAndReturn(func(p *cloudstack.RemoveFromLoadBalancerRuleParams) (*cloudstack.RemoveFromLoadBalancerRuleResponse, error) {
		ids, _ := p.GetVirtualmachineids()
		if len(ids) != 1 || ids[0] != "old" {
			t.Fatalf("healthy backend removed: %v", ids)
		}
		removed = true
		return &cloudstack.RemoveFromLoadBalancerRuleResponse{}, nil
	})
	cs := &CSCloud{client: &cloudstack.CloudStackClient{VirtualMachine: vm, LoadBalancer: l}, backendProbe: func(context.Context, string) error {
		if !removed {
			t.Fatal("removal blocked by new backend")
		}
		return errors.New("not ready")
	}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default", UID: "uid"}}
	if err := cs.UpdateLoadBalancer(context.Background(), "test", svc, nodesNamed("node-keep", "node-new")); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected retry error: %v", err)
	}
	if !removed {
		t.Fatal("old backend was not removed")
	}
}

func TestBackendReadinessUsesGuestNICAndRejectsMissingNIC(t *testing.T) {
	for _, missing := range []bool{false, true} {
		ctrl := gomock.NewController(t)
		vm := cloudstack.NewMockVirtualMachineServiceIface(ctrl)
		nics := []cloudstack.Nic{{Networkid: "net", Ipaddress: "10.123.6.142"}}
		if missing {
			nics = nil
		}
		vm.EXPECT().NewListVirtualMachinesParams().Return(&cloudstack.ListVirtualMachinesParams{})
		vm.EXPECT().ListVirtualMachines(gomock.Any()).Return(&cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{{Id: "new", Name: "node-new", Nic: nics}}}, nil)
		cs := &CSCloud{client: &cloudstack.CloudStackClient{VirtualMachine: vm}}
		ids, network, ips, err := cs.verifyHostsWithAddresses(nodesNamed("node-new"))
		if missing {
			if err == nil || !strings.Contains(err.Error(), "no NIC") {
				t.Fatalf("missing NIC: %v", err)
			}
		} else if err != nil || len(ids) != 1 || network != "net" || net.JoinHostPort(ips["new"], strconv.Itoa(30208)) != "10.123.6.142:30208" {
			t.Fatalf("incorrect API NIC mapping: %v %v %v %v", ids, network, ips, err)
		}
	}
}
