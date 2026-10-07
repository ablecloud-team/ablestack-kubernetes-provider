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
	"strings"

	"github.com/ablecloud-team/ablestack-mold-go/v2/cloudstack"
	cloudprovider "k8s.io/cloud-provider"
)

// lifecycleInstance uses the list API directly: the SDK lookup helper returns count=0
// for both a missing VM and an API error, which is unsafe for node deletion.
func (cs *CSCloud) lifecycleInstance(ctx context.Context, providerID, nodeName string) (*cloudstack.VirtualMachine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(providerID)
	if id != "" {
		if strings.Contains(id, "://") {
			parts := strings.Split(id, "://")
			if len(parts) != 2 || parts[0] != cs.ProviderName() {
				return nil, errors.New("invalid lifecycle provider ID")
			}
			id = parts[1]
		}
		if id == "" || strings.ContainsAny(id, "/?# \t\r\n") {
			return nil, errors.New("invalid lifecycle instance ID")
		}
	} else if strings.TrimSpace(nodeName) == "" {
		return nil, errors.New("node has neither a provider ID nor a name")
	}
	params := cs.client.VirtualMachine.NewListVirtualMachinesParams()
	params.SetListall(true)
	if cs.projectID != "" {
		params.SetProjectid(cs.projectID)
	}
	if id != "" {
		params.SetId(id)
	} else {
		params.SetName(nodeName)
	}
	response, err := cs.client.VirtualMachine.ListVirtualMachines(params)
	if err != nil {
		return nil, fmt.Errorf("cannot determine VM lifecycle: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("empty VM lifecycle API response")
	}
	if len(response.VirtualMachines) == 0 {
		if response.Count != 0 {
			return nil, errors.New("inconsistent VM lifecycle API count")
		}
		return nil, nil
	}
	if len(response.VirtualMachines) != 1 || response.Count != 1 {
		return nil, errors.New("ambiguous VM lifecycle API response")
	}
	instance := response.VirtualMachines[0]
	if instance == nil || instance.Id == "" {
		return nil, errors.New("incomplete VM lifecycle API response")
	}
	if (id != "" && instance.Id != id) || (id == "" && instance.Name != nodeName) {
		return nil, errors.New("VM lifecycle API returned a different instance")
	}
	return instance, nil
}

func lifecycleShutdown(instance *cloudstack.VirtualMachine, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	if instance == nil {
		return false, cloudprovider.InstanceNotFound
	}
	switch instance.State {
	case "Stopped":
		return true, nil
	case "Running", "Starting", "Stopping", "Migrating", "Destroyed", "Expunging":
		// Transition/deletion states do not prove that a disk can safely be detached.
		return false, nil
	default:
		return false, fmt.Errorf("VM shutdown cannot be determined from state %q", instance.State)
	}
}
