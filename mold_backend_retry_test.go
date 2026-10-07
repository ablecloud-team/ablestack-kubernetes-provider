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
	"errors"
	"testing"

	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	"github.com/blang/semver/v4"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
)

func TestEnsureExistingRuleRepairsFailedFirstAssignment(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := cloudstack.NewMockLoadBalancerServiceIface(ctrl)
	list := &cloudstack.ListLoadBalancerRuleInstancesParams{}
	assign := &cloudstack.AssignToLoadBalancerRuleParams{}
	rule := &cloudstack.LoadBalancerRule{Id: "rule-1", Name: "test", Privateport: "32130"}
	lb := &loadBalancer{CloudStackClient: &cloudstack.CloudStackClient{LoadBalancer: mock}, hostIDs: []string{"worker-1"}}
	attempts := 0
	lb.beforeAssign = func(*cloudstack.LoadBalancerRule, []string) error {
		attempts++
		if attempts == 1 {
			return errors.New("NodePort is not ready")
		}
		return nil
	}
	gomock.InOrder(
		mock.EXPECT().NewListLoadBalancerRuleInstancesParams("rule-1").Return(list),
		mock.EXPECT().ListLoadBalancerRuleInstances(list).Return(&cloudstack.ListLoadBalancerRuleInstancesResponse{}, nil),
		mock.EXPECT().NewListLoadBalancerRuleInstancesParams("rule-1").Return(list),
		mock.EXPECT().ListLoadBalancerRuleInstances(list).Return(&cloudstack.ListLoadBalancerRuleInstancesResponse{}, nil),
		mock.EXPECT().NewAssignToLoadBalancerRuleParams("rule-1").Return(assign),
		mock.EXPECT().AssignToLoadBalancerRule(assign).Return(&cloudstack.AssignToLoadBalancerRuleResponse{}, nil),
		mock.EXPECT().NewListLoadBalancerRuleInstancesParams("rule-1").Return(list),
		mock.EXPECT().ListLoadBalancerRuleInstances(list).Return(&cloudstack.ListLoadBalancerRuleInstancesResponse{Count: 1, LoadBalancerRuleInstances: []*cloudstack.VirtualMachine{{Id: "worker-1"}}}, nil),
	)
	d := desiredLBRule{name: "test", existing: rule, change: ruleUpToDate}
	if _, err := lb.ensureLoadBalancerRule(d, &corev1.Service{}, semver.MustParse("4.23.0")); err == nil {
		t.Fatal("unready backend must fail")
	}
	if _, err := lb.ensureLoadBalancerRule(d, &corev1.Service{}, semver.MustParse("4.23.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.ensureLoadBalancerRule(d, &corev1.Service{}, semver.MustParse("4.23.0")); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("converged rule reprobed or assignment skipped: %d", attempts)
	}
}

func TestExistingRuleBackendAdditionBeforeRemoval(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement-ready", true: "replacement-failed"}[fail], func(t *testing.T) {
			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)
			mock := cloudstack.NewMockLoadBalancerServiceIface(ctrl)
			list := &cloudstack.ListLoadBalancerRuleInstancesParams{}
			assign := &cloudstack.AssignToLoadBalancerRuleParams{}
			remove := &cloudstack.RemoveFromLoadBalancerRuleParams{}
			rule := &cloudstack.LoadBalancerRule{Id: "rule-1"}
			lb := &loadBalancer{CloudStackClient: &cloudstack.CloudStackClient{LoadBalancer: mock}, hostIDs: []string{"worker-new"}}
			var apiErr error
			if fail {
				apiErr = errors.New("assignment failed")
			}
			seq := []any{
				mock.EXPECT().NewListLoadBalancerRuleInstancesParams("rule-1").Return(list),
				mock.EXPECT().ListLoadBalancerRuleInstances(list).Return(&cloudstack.ListLoadBalancerRuleInstancesResponse{Count: 1, LoadBalancerRuleInstances: []*cloudstack.VirtualMachine{{Id: "worker-old"}}}, nil),
				mock.EXPECT().NewAssignToLoadBalancerRuleParams("rule-1").Return(assign),
				mock.EXPECT().AssignToLoadBalancerRule(assign).Return(&cloudstack.AssignToLoadBalancerRuleResponse{}, apiErr),
			}
			if !fail {
				seq = append(seq, mock.EXPECT().NewRemoveFromLoadBalancerRuleParams("rule-1").Return(remove), mock.EXPECT().RemoveFromLoadBalancerRule(remove).Return(&cloudstack.RemoveFromLoadBalancerRuleResponse{}, nil))
			}
			gomock.InOrder(seq...)
			err := lb.reconcileExistingRuleHosts(rule)
			if (err != nil) != fail {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestExistingRuleBackendReadErrorDoesNotMutate(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := cloudstack.NewMockLoadBalancerServiceIface(ctrl)
	list := &cloudstack.ListLoadBalancerRuleInstancesParams{}
	mock.EXPECT().NewListLoadBalancerRuleInstancesParams("rule-1").Return(list)
	mock.EXPECT().ListLoadBalancerRuleInstances(list).Return(nil, errors.New("read failed"))
	lb := &loadBalancer{CloudStackClient: &cloudstack.CloudStackClient{LoadBalancer: mock}, hostIDs: []string{"worker-1"}}
	if err := lb.reconcileExistingRuleHosts(&cloudstack.LoadBalancerRule{Id: "rule-1"}); err == nil {
		t.Fatal("read error must fail closed")
	}
}
