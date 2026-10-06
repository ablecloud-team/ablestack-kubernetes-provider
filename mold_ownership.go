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
	"fmt"
	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	corev1 "k8s.io/api/core/v1"
)

const (
	ownerClusterTag    = "mold.k8s.cluster-uid"
	ownerServiceTag    = "mold.k8s.service-uid"
	ownerNetworkTag    = "mold.k8s.network-uid"
	ownerGenerationTag = "mold.k8s.ip-generation"
	ownerIPTag         = "mold.k8s.ip-uid"
)

func ownershipTagMap(tags []cloudstack.Tags) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		out[tag.Key] = tag.Value
	}
	return out
}
func (lb *loadBalancer) ownershipTags() []cloudstack.Tags {
	return []cloudstack.Tags{{Key: ownerClusterTag, Value: lb.clusterUID}, {Key: ownerServiceTag, Value: lb.serviceUID}, {Key: ownerNetworkTag, Value: lb.networkID}, {Key: ownerGenerationTag, Value: lb.ipGeneration}, {Key: ownerIPTag, Value: lb.ipAddrID}}
}
func (lb *loadBalancer) ownsResource(tags []cloudstack.Tags) bool {
	if lb.clusterUID == "" {
		return true
	}
	m := ownershipTagMap(tags)
	return lb.ipAddrID != "" && m[ownerIPTag] == lb.ipAddrID && lb.serviceUID != "" && lb.networkID != "" && lb.ipGeneration != "" && m[ownerClusterTag] == lb.clusterUID && m[ownerServiceTag] == lb.serviceUID && m[ownerNetworkTag] == lb.networkID && m[ownerGenerationTag] == lb.ipGeneration
}
func (lb *loadBalancer) tagOwnedResource(kind, id string) error {
	if lb.clusterUID == "" {
		return nil
	}
	if lb.serviceUID == "" || lb.networkID == "" || lb.ipGeneration == "" || lb.ipAddrID == "" || id == "" {
		return fmt.Errorf("cannot record incomplete ownership receipt")
	}
	tags := ownershipTagMap(lb.ownershipTags())
	_, err := lb.Resourcetags.CreateTags(lb.Resourcetags.NewCreateTagsParams([]string{id}, kind, tags))
	return err
}
func (lb *loadBalancer) verifyAllocationReceipt() error {
	if lb.clusterUID == "" || lb.ipAddrID == "" {
		return nil
	}
	ip, count, err := lb.Address.GetPublicIpAddressByID(lb.ipAddrID, cloudstack.WithProject(lb.projectID))
	if err != nil || count != 1 {
		return fmt.Errorf("cannot verify public IP allocation")
	}
	if lb.ipGeneration == "" || ip.Allocationgeneration != lb.ipGeneration {
		return fmt.Errorf("public IP allocation changed; preserving resources")
	}
	return nil
}
func (lb *loadBalancer) releaseOwnedAllocation() error {
	if lb.ipAddrID == "" {
		return nil
	}
	ip, count, err := lb.Address.GetPublicIpAddressByID(lb.ipAddrID, cloudstack.WithProject(lb.projectID))
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	tags := ownershipTagMap(ip.Tags)
	// Manually allocated/shared IPs are retained. Another service may be the first
	// allocator, so the cluster/network/allocation identity owns this receipt.
	if tags[ownerClusterTag] != lb.clusterUID || tags[ownerNetworkTag] != lb.networkID {
		return nil
	}
	generation := tags[ownerGenerationTag]
	if generation == "" || generation != ip.Allocationgeneration {
		return fmt.Errorf("public IP allocation receipt changed")
	}
	if ip.Issourcenat || ip.Isstaticnat || ip.Isportable {
		return nil
	}
	lbp := lb.LoadBalancer.NewListLoadBalancerRulesParams()
	lbp.SetPublicipid(lb.ipAddrID)
	lbp.SetListall(true)
	lbs, err := lb.LoadBalancer.ListLoadBalancerRules(lbp)
	if err != nil {
		return err
	}
	if lbs.Count > 0 {
		return nil
	}
	fwp := lb.Firewall.NewListFirewallRulesParams()
	fwp.SetIpaddressid(lb.ipAddrID)
	fwp.SetListall(true)
	fws, err := lb.Firewall.ListFirewallRules(fwp)
	if err != nil {
		return err
	}
	if fws.Count > 0 {
		return nil
	}
	pfp := lb.Firewall.NewListPortForwardingRulesParams()
	pfp.SetIpaddressid(lb.ipAddrID)
	pfp.SetListall(true)
	pfs, err := lb.Firewall.ListPortForwardingRules(pfp)
	if err != nil {
		return err
	}
	if pfs.Count > 0 {
		return nil
	}
	p := lb.Address.NewDisassociateIpAddressParams(lb.ipAddrID)
	p.SetExpectedallocationgeneration(generation)
	_, err = lb.Address.DisassociateIpAddress(p)
	return err
}

// Recover a remaining tagged IP when an earlier attempt already deleted the LB.
// This is a read-only lookup; deletion must never associate an unallocated IP.
func (lb *loadBalancer) restoreOwnedAllocationFromService(service *corev1.Service) error {
	if lb.clusterUID == "" || lb.ipAddrID != "" || len(service.Status.LoadBalancer.Ingress) == 0 {
		return nil
	}
	address := service.Status.LoadBalancer.Ingress[0].IP
	if address == "" {
		return nil
	}
	p := lb.Address.NewListPublicIpAddressesParams()
	p.SetIpaddress(address)
	p.SetAllocatedonly(true)
	p.SetListall(true)
	if lb.projectID != "" {
		p.SetProjectid(lb.projectID)
	}
	result, err := lb.Address.ListPublicIpAddresses(p)
	if err != nil {
		return err
	}
	if result.Count == 0 {
		return nil
	}
	if result.Count != 1 {
		return fmt.Errorf("ambiguous public IP cleanup receipt")
	}
	ip := result.PublicIpAddresses[0]
	tags := ownershipTagMap(ip.Tags)
	if tags[ownerClusterTag] != lb.clusterUID {
		return nil
	}
	if tags[ownerNetworkTag] == "" || tags[ownerGenerationTag] == "" || tags[ownerGenerationTag] != ip.Allocationgeneration {
		return fmt.Errorf("remaining allocation receipt changed")
	}
	lb.ipAddrID, lb.ipAddr, lb.networkID, lb.ipGeneration = ip.Id, ip.Ipaddress, tags[ownerNetworkTag], tags[ownerGenerationTag]
	return nil
}
