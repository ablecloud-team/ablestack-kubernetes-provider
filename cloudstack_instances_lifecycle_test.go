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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cloudprovider "k8s.io/cloud-provider"
)

func lifecycleTestCloud(t *testing.T, response *cloudstack.ListVirtualMachinesResponse, apiErr error, id, name, project string, after func()) *CSCloud {
	t.Helper()
	vm := cloudstack.NewMockVirtualMachineServiceIface(gomock.NewController(t))
	vm.EXPECT().NewListVirtualMachinesParams().Return(&cloudstack.ListVirtualMachinesParams{})
	vm.EXPECT().ListVirtualMachines(gomock.Any()).DoAndReturn(func(p *cloudstack.ListVirtualMachinesParams) (*cloudstack.ListVirtualMachinesResponse, error) {
		gotID, _ := p.GetId()
		gotName, _ := p.GetName()
		gotProject, _ := p.GetProjectid()
		all, _ := p.GetListall()
		if gotID != id || gotName != name || gotProject != project || !all {
			t.Fatalf("wrong lifecycle lookup: id=%q name=%q project=%q listAll=%v", gotID, gotName, gotProject, all)
		}
		if after != nil {
			after()
		}
		return response, apiErr
	})
	return &CSCloud{client: &cloudstack.CloudStackClient{VirtualMachine: vm}, projectID: project}
}

func TestLifecycleShutdownStates(t *testing.T) {
	for _, state := range []string{"Stopped", "Running", "Starting", "Stopping", "Migrating", "Destroyed", "Expunging", "Error", "Unknown", "", "future-state"} {
		t.Run(state, func(t *testing.T) {
			response := &cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{{Id: "vm-id", State: state}}}
			cs := lifecycleTestCloud(t, response, nil, "vm-id", "", "project-id", nil)
			stopped, err := cs.InstanceShutdownByProviderID(context.Background(), "external-cloudstack://vm-id")
			wantError := state == "Error" || state == "Unknown" || state == "" || state == "future-state"
			if stopped != (state == "Stopped") || (err != nil) != wantError {
				t.Fatalf("state %q: shutdown=%v error=%v", state, stopped, err)
			}
		})
	}
}

func TestLifecycleLookupUncertaintyIsNeverAbsence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *cloudstack.ListVirtualMachinesResponse
		apiErr   error
	}{
		{name: "authentication failure", apiErr: errors.New("HTTP 401")},
		{name: "authorization failure", apiErr: errors.New("HTTP 403")},
		{name: "transport failure", apiErr: errors.New("network timeout")},
		{name: "nil response"},
		{name: "nil VM", response: &cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{nil}}},
		{name: "missing VM ID", response: &cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{{}}}},
		{name: "wrong VM ID", response: &cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{{Id: "other"}}}},
		{name: "inconsistent count", response: &cloudstack.ListVirtualMachinesResponse{Count: 1}},
		{name: "negative count", response: &cloudstack.ListVirtualMachinesResponse{Count: -1}},
		{name: "VM without count", response: &cloudstack.ListVirtualMachinesResponse{VirtualMachines: []*cloudstack.VirtualMachine{{Id: "vm-id"}}}},
		{name: "multiple VMs", response: &cloudstack.ListVirtualMachinesResponse{Count: 2, VirtualMachines: []*cloudstack.VirtualMachine{{Id: "vm-id"}, {Id: "other"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := lifecycleTestCloud(t, tc.response, tc.apiErr, "vm-id", "", "", nil)
			exists, err := cs.InstanceExistsByProviderID(context.Background(), "vm-id")
			if exists || err == nil {
				t.Fatalf("uncertainty became a definitive lifecycle result: exists=%v error=%v", exists, err)
			}
		})
	}
	cs := lifecycleTestCloud(t, &cloudstack.ListVirtualMachinesResponse{}, nil, "vm-id", "", "", nil)
	exists, err := cs.InstanceExistsByProviderID(context.Background(), "vm-id")
	if exists || err != nil {
		t.Fatalf("successful empty response did not report absence: %v %v", exists, err)
	}
	cs = lifecycleTestCloud(t, &cloudstack.ListVirtualMachinesResponse{}, nil, "vm-id", "", "", nil)
	stopped, err := cs.InstanceShutdownByProviderID(context.Background(), "vm-id")
	if stopped || !errors.Is(err, cloudprovider.InstanceNotFound) {
		t.Fatalf("absence became a safe shutdown: %v %v", stopped, err)
	}
	// A stopped or transitional VM still exists; the CCM must keep its Node.
	for _, state := range []string{"Stopped", "Stopping", "Unknown", "Destroyed"} {
		cs = lifecycleTestCloud(t, &cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{{Id: "vm-id", State: state}}}, nil, "vm-id", "", "", nil)
		exists, err = cs.InstanceExistsByProviderID(context.Background(), "vm-id")
		if !exists || err != nil {
			t.Fatalf("existing %s VM became missing: %v %v", state, exists, err)
		}
	}
}

func TestLifecycleNodeLookupPrefersProviderID(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "old-hostname"}, Spec: corev1.NodeSpec{ProviderID: "external-cloudstack://vm-id"}}
	response := &cloudstack.ListVirtualMachinesResponse{Count: 1, VirtualMachines: []*cloudstack.VirtualMachine{{Id: "vm-id", Name: "new-hostname", State: "Stopped"}}}
	cs := lifecycleTestCloud(t, response, nil, "vm-id", "", "project", nil)
	exists, err := cs.InstanceExists(context.Background(), node)
	if !exists || err != nil {
		t.Fatalf("provider ID was not used: %v %v", exists, err)
	}
	cs = lifecycleTestCloud(t, response, nil, "vm-id", "", "project", nil)
	stopped, err := cs.InstanceShutdown(context.Background(), node)
	if !stopped || err != nil {
		t.Fatalf("V2 shutdown did not use provider ID: %v %v", stopped, err)
	}
	node.Spec.ProviderID = ""
	node.Name = "new-hostname"
	cs = lifecycleTestCloud(t, response, nil, "", "new-hostname", "project", nil)
	stopped, err = cs.InstanceShutdown(context.Background(), node)
	if !stopped || err != nil {
		t.Fatalf("uninitialized name fallback failed: %v %v", stopped, err)
	}
	node.Name = "new"
	cs = lifecycleTestCloud(t, response, nil, "", "new", "project", nil)
	exists, err = cs.InstanceExists(context.Background(), node)
	if exists || err == nil {
		t.Fatalf("prefix name match became authoritative: %v %v", exists, err)
	}
}

func TestLifecycleRejectsInvalidAndCanceledLookups(t *testing.T) {
	cs := &CSCloud{}
	for _, id := range []string{"", "external-cloudstack://", "foreign://vm-id", "external-cloudstack://vm-id/other", "vm?query", "external-cloudstack://vm-id://extra"} {
		if _, err := cs.InstanceExistsByProviderID(context.Background(), id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	if _, err := cs.InstanceExists(context.Background(), nil); err == nil {
		t.Fatal("accepted nil Node")
	}
	if _, err := cs.InstanceShutdown(context.Background(), &corev1.Node{}); err == nil {
		t.Fatal("accepted empty Node")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cs.InstanceExistsByProviderID(ctx, "vm-id"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cs = lifecycleTestCloud(t, &cloudstack.ListVirtualMachinesResponse{}, nil, "vm-id", "", "", cancel)
	exists, err := cs.InstanceExistsByProviderID(ctx, "vm-id")
	if exists || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled API result became absence: %v %v", exists, err)
	}
}

func TestLifecycleRealSDKHTTPFailuresDoNotDeleteNode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "{\"errorresponse\":{\"errorcode\":401,\"errortext\":\"authentication failed\"}}"},
		{"unavailable", 503, "temporarily unavailable"},
		{"API error", 200, "{\"errorresponse\":{\"errorcode\":530,\"errortext\":\"backend error\"}}"},
		{"malformed response", 200, "not-json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer server.Close()
			cs := &CSCloud{client: cloudstack.NewClient(server.URL, "test-key", "test-secret", false)}
			exists, err := cs.InstanceExistsByProviderID(context.Background(), "vm-id")
			if exists || err == nil {
				t.Fatalf("HTTP/API failure allowed node deletion: %v %v", exists, err)
			}
		})
	}
	// The same real SDK may report absence only on a successful empty VM list.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.URL.Query().Get("command"), "listVirtualMachines") {
			t.Errorf("wrong API command")
		}
		w.Write([]byte("{\"listvirtualmachinesresponse\":{\"count\":0,\"virtualmachine\":[]}}"))
	}))
	defer server.Close()
	cs := &CSCloud{client: cloudstack.NewClient(server.URL, "test-key", "test-secret", false)}
	exists, err := cs.InstanceExistsByProviderID(context.Background(), "vm-id")
	if exists || err != nil {
		t.Fatalf("genuine empty list: %v %v", exists, err)
	}
}
