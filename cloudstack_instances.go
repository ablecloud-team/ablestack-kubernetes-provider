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
	"fmt"
	"regexp"

	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

var labelInvalidCharsRegex *regexp.Regexp = regexp.MustCompile(`([^A-Za-z0-9][^-A-Za-z0-9_.]*)?[^A-Za-z0-9]`)

// NodeAddresses returns the addresses of the specified instance.
func (cs *CSCloud) NodeAddresses(ctx context.Context, name types.NodeName) ([]corev1.NodeAddress, error) {
	instance, count, err := cs.client.VirtualMachine.GetVirtualMachineByName(
		string(name),
		cloudstack.WithProject(cs.projectID),
	)
	if err != nil {
		if count == 0 {
			return nil, cloudprovider.InstanceNotFound
		}
		return nil, fmt.Errorf("error retrieving node addresses: %v", err)
	}

	return cs.nodeAddresses(instance)
}

// NodeAddressesByProviderID returns the addresses of the specified instance.
func (cs *CSCloud) NodeAddressesByProviderID(ctx context.Context, providerID string) ([]corev1.NodeAddress, error) {
	instance, count, err := cs.client.VirtualMachine.GetVirtualMachineByID(
		providerID,
		cloudstack.WithProject(cs.projectID),
	)
	if err != nil {
		if count == 0 {
			return nil, cloudprovider.InstanceNotFound
		}
		return nil, fmt.Errorf("error retrieving node addresses: %v", err)
	}

	return cs.nodeAddresses(instance)
}

func (cs *CSCloud) nodeAddresses(instance *cloudstack.VirtualMachine) ([]corev1.NodeAddress, error) {
	if len(instance.Nic) == 0 {
		return nil, errors.New("instance does not have an internal IP")
	}

	addresses := []corev1.NodeAddress{
		{Type: corev1.NodeInternalIP, Address: instance.Nic[0].Ipaddress},
	}

	if instance.Hostname != "" {
		addresses = append(addresses, corev1.NodeAddress{Type: corev1.NodeHostName, Address: instance.Hostname})
	}

	if instance.Publicip != "" {
		addresses = append(addresses, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: instance.Publicip})
	} else {
		// Since there is no sane way to determine the external IP if the host isn't
		// using static NAT, we will just fire a log message and omit the external IP.
		klog.V(4).Infof("Could not determine the public IP of host %v (%v)", instance.Name, instance.Id)
	}

	return addresses, nil
}

// InstanceID returns the cloud provider ID of the specified instance.
func (cs *CSCloud) InstanceID(ctx context.Context, name types.NodeName) (string, error) {
	instance, count, err := cs.client.VirtualMachine.GetVirtualMachineByName(
		string(name),
		cloudstack.WithProject(cs.projectID),
	)
	if err != nil {
		if count == 0 {
			return "", cloudprovider.InstanceNotFound
		}
		return "", fmt.Errorf("error retrieving instance ID: %v", err)
	}

	return instance.Id, nil
}

// InstanceType returns the type of the specified instance.
func (cs *CSCloud) InstanceType(ctx context.Context, name types.NodeName) (string, error) {
	instance, count, err := cs.client.VirtualMachine.GetVirtualMachineByName(
		string(name),
		cloudstack.WithProject(cs.projectID),
	)
	if err != nil {
		if count == 0 {
			return "", cloudprovider.InstanceNotFound
		}
		return "", fmt.Errorf("error retrieving instance type: %v", err)
	}

	return labelInvalidCharsRegex.ReplaceAllString(instance.Serviceofferingname, ``), nil
}

// InstanceTypeByProviderID returns the type of the specified instance.
func (cs *CSCloud) InstanceTypeByProviderID(ctx context.Context, providerID string) (string, error) {
	instance, count, err := cs.client.VirtualMachine.GetVirtualMachineByID(
		providerID,
		cloudstack.WithProject(cs.projectID),
	)
	if err != nil {
		if count == 0 {
			return "", cloudprovider.InstanceNotFound
		}
		return "", fmt.Errorf("error retrieving instance type: %v", err)
	}

	return labelInvalidCharsRegex.ReplaceAllString(instance.Serviceofferingname, ``), nil
}

// AddSSHKeyToAllInstances is currently not implemented.
func (cs *CSCloud) AddSSHKeyToAllInstances(ctx context.Context, user string, keyData []byte) error {
	return cloudprovider.NotImplemented
}

// CurrentNodeName returns the name of the node we are currently running on.
func (cs *CSCloud) CurrentNodeName(ctx context.Context, hostname string) (types.NodeName, error) {
	return types.NodeName(hostname), nil
}

// InstanceExistsByProviderID returns if the instance still exists.
func (cs *CSCloud) InstanceExistsByProviderID(ctx context.Context, providerID string) (bool, error) {
	_, count, err := cs.client.VirtualMachine.GetVirtualMachineByID(
		providerID,
		cloudstack.WithProject(cs.projectID),
	)
	if err != nil {
		if count == 0 {
			return false, nil
		}
		return false, fmt.Errorf("error retrieving instance: %v", err)
	}

	return true, nil
}

// InstanceShutdownByProviderID returns true if the instance is in safe state to detach volumes
func (cs *CSCloud) InstanceShutdownByProviderID(ctx context.Context, providerID string) (bool, error) {
	return false, cloudprovider.NotImplemented
}

func (cs *CSCloud) InstanceExists(ctx context.Context, node *corev1.Node) (bool, error) {
	nodeName := types.NodeName(node.Name)
	providerID, err := cs.InstanceID(ctx, nodeName)
	if err != nil {
		return false, err
	}

	return cs.InstanceExistsByProviderID(ctx, providerID)
}

func (cs *CSCloud) InstanceShutdown(ctx context.Context, node *corev1.Node) (bool, error) {
	return false, cloudprovider.NotImplemented
}

func (cs *CSCloud) InstanceMetadata(ctx context.Context, node *corev1.Node) (*cloudprovider.InstanceMetadata, error) {

	instanceType, err := cs.InstanceType(ctx, types.NodeName(node.Name))
	if err != nil {
		return nil, err
	}

	addresses, err := cs.NodeAddresses(ctx, types.NodeName(node.Name))
	if err != nil {
		return nil, err
	}

	zone, err := cs.GetZone(ctx)
	if err != nil {
		return nil, err
	}

	return &cloudprovider.InstanceMetadata{
		ProviderID:    cs.ProviderName(),
		InstanceType:  instanceType,
		NodeAddresses: addresses,
		Zone:          cs.zone,
		Region:        zone.Region,
	}, nil
}
