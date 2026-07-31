/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gpu

import (
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// DriverName is the DRA driver ghostgpu publishes simulated devices under.
//
// No kubelet plugin is registered for it, and none is needed: the scheduler
// performs allocation from published ResourceSlices, and kube-controller-manager
// handles deallocation. NodePrepareResources is container setup only, which
// kwok skips because it drives pods to Running directly.
const DriverName = "gpu.ghostgpu.dev"

// DRA device attribute names ghostgpu publishes.
//
// Consumers write CEL selectors against these, so the names are an external
// contract: renaming one silently breaks every selector in the wild that used
// it. They are shared by the whole-GPU and MIG paths so both stay consistent.
const (
	AttrProductName   = "productName"
	AttrVendor        = "vendor"
	AttrUUID          = "uuid"
	AttrNVLinkDomain  = "nvlinkDomain"
	AttrNUMANode      = "numaNode"
	AttrMIGProfile    = "migProfile"
	AttrMIGInstanceID = "migInstanceID"
)

// MaxDevicesPerSlice is the DRA limit on devices in a single ResourceSlice.
// It drops to 64 for devices that use taints or consume counters, which will
// matter when MIG lands and expands each GPU into multiple profiles.
const MaxDevicesPerSlice = 128

// SliceName is the ResourceSlice object name for a pool on a given node.
func SliceName(poolName, nodeName string) string {
	return fmt.Sprintf("%s-%s", poolName, nodeName)
}

func stringAttr(s string) resourcev1.DeviceAttribute {
	v := s
	return resourcev1.DeviceAttribute{StringValue: &v}
}

func intAttr(i int64) resourcev1.DeviceAttribute {
	v := i
	return resourcev1.DeviceAttribute{IntValue: &v}
}

// BuildResourceSlice constructs the ResourceSlice advertising one node's
// simulated GPUs.
//
// ghostgpu publishes one DRA pool per node, so Pool.Name is the node name.
// That keeps each node independently reconcilable and each slice well under
// MaxDevicesPerSlice, avoiding the sharding that a cluster-wide pool would
// eventually force.
//
// state is what the pool declares about this node's GPUs — how many are busy,
// and how many have failed. Both are applied lowest index first, so the same
// declaration always produces the same fleet rather than one that depends on
// iteration order.
//
// The result is a pure function of its inputs, so a restarted operator
// republishes rather than churns — which is also what makes declared occupancy
// and declared faults survive a restart.
func BuildResourceSlice(
	pool *v1alpha1.GPUPool,
	model *v1alpha1.GPUModel,
	nodeName string,
	state NodeState,
) *resourcev1.ResourceSlice {
	devices := make([]resourcev1.Device, 0, pool.Spec.GPUsPerNode)

	for i := range pool.Spec.GPUsPerNode {
		attrs := map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			AttrProductName: stringAttr(model.Spec.ProductName),
			AttrVendor:      stringAttr(model.Spec.Vendor),
			AttrUUID:        stringAttr(DeviceUUID(nodeName, i)),
		}

		// Topology attributes are omitted rather than zero-valued when
		// disabled, so a selector for them matches nothing instead of
		// matching everything at domain-0.
		if domain := NVLinkDomain(i, pool.Spec.Topology.NVLinkDomainSize); domain != "" {
			attrs[AttrNVLinkDomain] = stringAttr(domain)
		}
		if pool.Spec.Topology.NUMAAware {
			attrs[AttrNUMANode] = intAttr(NUMANode(i, pool.Spec.Topology.NVLinkDomainSize))
		}

		device := resourcev1.Device{
			Name:       DeviceName(nodeName, i),
			Attributes: attrs,
			Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
				"memory": {Value: model.Spec.Memory},
			},
		}
		if taint := state.Taint(i); taint != nil {
			device.Taints = []resourcev1.DeviceTaint{*taint}
		}
		devices = append(devices, device)
	}

	node := nodeName
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: SliceName(pool.Name, nodeName),
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:   DriverName,
			NodeName: &node,
			Pool: resourcev1.ResourcePool{
				Name:               nodeName,
				Generation:         1,
				ResourceSliceCount: 1,
			},
			Devices: devices,
		},
	}
}
