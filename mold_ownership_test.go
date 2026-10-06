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
	"encoding/json"
	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMoldResourceOwnershipRequiresEveryIdentity(t *testing.T) {
	lb := &loadBalancer{clusterUID: "cluster-1", serviceUID: "service-1", networkID: "network-1", ipGeneration: "generation-1", ipAddrID: "ip-1"}
	if !lb.ownsResource(lb.ownershipTags()) {
		t.Fatal("complete ownership rejected")
	}
	for _, key := range []string{ownerClusterTag, ownerServiceTag, ownerNetworkTag, ownerGenerationTag, ownerIPTag} {
		for _, value := range []string{"", "another-owner"} {
			tags := lb.ownershipTags()
			for i := range tags {
				if tags[i].Key == key {
					tags[i].Value = value
				}
			}
			if lb.ownsResource(tags) {
				t.Fatalf("accepted invalid %s", key)
			}
		}
	}
}
func TestMoldIPReleasePreservesManualSharedOrReusedAllocations(t *testing.T) {
	for _, tc := range []struct {
		name, shared              string
		manual, reused, sourceNat bool
		wantRelease               bool
	}{
		{name: "owned-empty", wantRelease: true},
		{name: "manual", manual: true},
		{name: "reused", reused: true},
		{name: "shared-lb", shared: "listloadbalancerrules"},
		{name: "shared-firewall", shared: "listfirewallrules"},
		{name: "shared-forward", shared: "listportforwardingrules"},
		{name: "source-nat", sourceNat: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			released := false
			tags := []map[string]string{{"key": ownerClusterTag, "value": "cluster-1"}, {"key": ownerNetworkTag, "value": "network-1"}, {"key": ownerGenerationTag, "value": "generation-1"}}
			if tc.manual {
				tags = nil
			}
			generation := "generation-1"
			if tc.reused {
				generation = "generation-2"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				command := strings.ToLower(r.FormValue("command"))
				var response interface{}
				switch command {
				case "listpublicipaddresses":
					response = map[string]interface{}{"count": 1, "publicipaddress": []interface{}{map[string]interface{}{"id": "ip-1", "ipaddress": "10.1.1.1", "allocated": "2026-10-06T19:00:00+0000", "allocationgeneration": generation, "issourcenat": tc.sourceNat, "tags": tags}}}
				case "listloadbalancerrules", "listfirewallrules", "listportforwardingrules":
					count := 0
					if command == tc.shared {
						count = 1
					}
					response = map[string]interface{}{"count": count}
				case "disassociateipaddress":
					released = true
					if r.FormValue("expectedallocationgeneration") != "generation-1" {
						t.Error("missing allocation precondition")
					}
					response = map[string]interface{}{"success": true}
				default:
					t.Errorf("unexpected API %s", command)
					response = map[string]interface{}{}
				}
				json.NewEncoder(w).Encode(map[string]interface{}{command + "response": response})
			}))
			defer server.Close()
			lb := &loadBalancer{CloudStackClient: cloudstack.NewClient(server.URL, "fixture", "fixture", true), clusterUID: "cluster-1", serviceUID: "service-1", networkID: "network-1", ipGeneration: "generation-1", ipAddrID: "ip-1"}
			err := lb.releaseOwnedAllocation()
			if released != tc.wantRelease {
				t.Fatalf("unexpected allocation release=%t error=%v", released, err)
			}
			if tc.reused && err == nil {
				t.Fatal("allocation mismatch not surfaced")
			}
			if !tc.reused && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMoldDuplicateOwnershipUsesThePublishedIPGeneration(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app", UID: types.UID("service-1")}, Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "10.1.1.1"}}}}}
	cs := &CSCloud{clusterUID: "cluster-1"}
	name := cs.GetLoadBalancerName(context.Background(), "", svc) + "-tcp-80"
	rule := func(id, ip, generation string) map[string]interface{} {
		lb := &loadBalancer{clusterUID: "cluster-1", serviceUID: "service-1", networkID: "network-1", ipGeneration: generation, ipAddrID: id + "-ip"}
		return map[string]interface{}{"id": id, "name": name, "publicip": ip, "publicipid": id + "-ip", "tags": lb.ownershipTags()}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.FormValue("command")) != "listloadbalancerrules" {
			t.Errorf("unexpected mutation or lookup")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"listloadbalancerrulesresponse": map[string]interface{}{"count": 2, "loadbalancerrule": []interface{}{rule("keep", "10.1.1.1", "generation-1"), rule("duplicate", "10.1.1.2", "generation-2")}}})
	}))
	defer server.Close()
	cs.client = cloudstack.NewClient(server.URL, "fixture", "fixture", true)
	lb, err := cs.getLoadBalancer(svc)
	if err != nil {
		t.Fatal(err)
	}
	if lb.ipAddrID != "keep-ip" || lb.ipGeneration != "generation-1" || len(lb.duplicateRules) != 1 {
		t.Fatalf("published IP ownership was overwritten: %+v", lb)
	}
	if !lb.ownsResource(lb.rules[name].Tags) {
		t.Fatal("kept rule rejected")
	}
	if lb.ownsResource(lb.duplicateRules[0].Tags) {
		t.Fatal("other allocation accepted as current")
	}
}

func TestMoldDeletionRetryDoesNotAllocateAnIP(t *testing.T) {
	calls := []string{}
	lb := &loadBalancer{clusterUID: "cluster-1", serviceUID: "service-1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		command := strings.ToLower(r.FormValue("command"))
		calls = append(calls, command)
		if command != "listpublicipaddresses" {
			t.Errorf("deletion made a mutating call %s", command)
		}
		if r.FormValue("allocatedonly") != "true" {
			t.Error("unallocated pool addresses included")
		}
		ip := map[string]interface{}{"id": "ip-1", "ipaddress": "10.1.1.1", "allocationgeneration": "generation-1", "tags": []cloudstack.Tags{{Key: ownerClusterTag, Value: "cluster-1"}, {Key: ownerNetworkTag, Value: "network-1"}, {Key: ownerGenerationTag, Value: "generation-1"}}}
		json.NewEncoder(w).Encode(map[string]interface{}{"listpublicipaddressesresponse": map[string]interface{}{"count": 1, "publicipaddress": []interface{}{ip}}})
	}))
	defer server.Close()
	lb.CloudStackClient = cloudstack.NewClient(server.URL, "fixture", "fixture", true)
	svc := &corev1.Service{Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "10.1.1.1"}}}}}
	if err := lb.restoreOwnedAllocationFromService(svc); err != nil {
		t.Fatal(err)
	}
	if lb.ipAddrID != "ip-1" || len(calls) != 1 {
		t.Fatal("remaining allocation not recovered read-only")
	}
}

func TestMoldLastServiceReleasesControllerOwnedSharedIPWithoutItsOldAnnotation(t *testing.T) {
	released := false
	owner := &loadBalancer{clusterUID: "cluster-1", serviceUID: "deleted-first-service", networkID: "network-1", ipAddrID: "ip-1", ipGeneration: "generation-1"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		command := strings.ToLower(r.FormValue("command"))
		var result interface{}
		switch command {
		case "listpublicipaddresses":
			result = map[string]interface{}{"count": 1, "publicipaddress": []interface{}{map[string]interface{}{"id": "ip-1", "ipaddress": "10.1.1.1", "allocated": "2026-10-06T19:00:00+0000", "allocationgeneration": "generation-1", "tags": owner.ownershipTags()}}}
		case "listloadbalancerrules", "listfirewallrules", "listportforwardingrules":
			result = map[string]interface{}{"count": 0}
		case "disassociateipaddress":
			released = true
			if r.FormValue("expectedallocationgeneration") != "generation-1" {
				t.Error("missing allocation generation")
			}
			result = map[string]interface{}{"success": true}
		default:
			t.Errorf("unexpected API %s", command)
			result = map[string]interface{}{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{command + "response": result})
	}))
	defer server.Close()
	cs := &CSCloud{clusterUID: "cluster-1", client: cloudstack.NewClient(server.URL, "fixture", "fixture", true)}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "last", Namespace: "app", UID: types.UID("last-service")}, Spec: corev1.ServiceSpec{LoadBalancerIP: "10.1.1.1"}, Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "10.1.1.1"}}}}}
	if err := cs.EnsureLoadBalancerDeleted(context.Background(), "", svc); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("last Service left a verified controller-owned allocation behind")
	}
}
