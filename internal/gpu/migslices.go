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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// Hard API limits, both measured against a live API server rather than read
// from documentation. See docs/superpowers/specs/2026-07-30-mig-sharding-findings.md.
const (
	// MaxCounterSetsPerSlice is the ceiling on sharedCounters in one
	// ResourceSlice. MIG needs one counter set per physical GPU, so this caps
	// a counter slice at 8 GPUs and is the tightest constraint in the design.
	MaxCounterSetsPerSlice = 8

	// MaxMIGDevicesPerSlice is the ceiling on devices in one ResourceSlice
	// when those devices consume counters. Devices without counters may go to
	// MaxDevicesPerSlice; every MIG device consumes counters, so this is the
	// number that governs here.
	MaxMIGDevicesPerSlice = 64
)

// Counter names within a GPU's shared counter set. Both dimensions are needed:
// the H100 offers two single-slice profiles differing only in memory, so
// neither counter can be derived from the other.
const (
	counterMemory = "memory"
	counterSlices = "slices"
)

// MIGCounterSliceName is the object name of a pool's Nth counter slice.
func MIGCounterSliceName(poolName, nodeName string, shard int) string {
	return fmt.Sprintf("%s-%s-counters-%d", poolName, nodeName, shard)
}

// MIGDeviceSliceName is the object name of a pool's Nth device slice.
func MIGDeviceSliceName(poolName, nodeName string, shard int) string {
	return fmt.Sprintf("%s-%s-devices-%d", poolName, nodeName, shard)
}

// BuildMIGSlices constructs every ResourceSlice advertising one node's
// simulated MIG instances.
//
// The layout is forced by two measured API limits: sharedCounters and devices
// cannot share a slice, at most 8 counter sets fit in one slice, and at most 64
// counter-consuming devices fit in one. So a node becomes
//
//	ceil(gpus / 8)              counter slices
//	ceil(gpus * profiles / 64)  device slices
//
// all in a single DRA pool. That is safe because counter sets resolve
// pool-wide: the spike verified that a device in one slice consumes a counter
// set declared in another, with a negative control, so a GPU's profiles may
// straddle a shard boundary freely.
//
// busy is how many of the node's physical GPUs are declared occupied. Every
// instance carved from those cards is tainted, not merely one: MIG instances
// draw on a shared counter set, so leaving a single profile allocatable on an
// "occupied" card would mean the card was never occupied at all.
//
// The result is a pure function of its inputs, so a restarted operator
// republishes identical slices rather than churning them.
func BuildMIGSlices(
	pool *v1alpha1.GPUPool,
	model *v1alpha1.GPUModel,
	table mig.Table,
	nodeName string,
	busy int32,
) []*resourcev1.ResourceSlice {
	gpus := pool.Spec.GPUsPerNode

	// A declared partition means static MIG: publish exactly the instances the
	// administrator created. Without one, publish every profile the hardware
	// supports and let the counters keep overlapping choices apart.
	instances := mig.Expand(gpus, table)
	if len(pool.Spec.MIGPartition) > 0 {
		instances = mig.ExpandPartitioned(gpus, table, pool.Spec.MIGPartition)
	}

	counterShards := ceilDiv(int(gpus), MaxCounterSetsPerSlice)
	deviceShards := ceilDiv(len(instances), MaxMIGDevicesPerSlice)
	total := int32(counterShards + deviceShards)

	slices := make([]*resourcev1.ResourceSlice, 0, counterShards+deviceShards)

	for shard := range counterShards {
		first := int32(shard * MaxCounterSetsPerSlice)
		last := min(first+MaxCounterSetsPerSlice, gpus)

		sets := make([]resourcev1.CounterSet, 0, last-first)
		for gpu := first; gpu < last; gpu++ {
			sets = append(sets, resourcev1.CounterSet{
				Name:     mig.CounterSetName(gpu),
				Counters: budgetCounters(table.Budget),
			})
		}

		slice := newMIGSlice(MIGCounterSliceName(pool.Name, nodeName, shard), nodeName, total)
		slice.Spec.SharedCounters = sets
		slices = append(slices, slice)
	}

	for shard := range deviceShards {
		first := shard * MaxMIGDevicesPerSlice
		last := min(first+MaxMIGDevicesPerSlice, len(instances))

		devices := make([]resourcev1.Device, 0, last-first)
		for _, instance := range instances[first:last] {
			device := migDevice(pool, model, instance, nodeName)
			if instance.GPUIndex < busy {
				device.Taints = []resourcev1.DeviceTaint{OccupiedTaint()}
			}
			devices = append(devices, device)
		}

		slice := newMIGSlice(MIGDeviceSliceName(pool.Name, nodeName, shard), nodeName, total)
		slice.Spec.Devices = devices
		slices = append(slices, slice)
	}

	return slices
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func newMIGSlice(name, nodeName string, total int32) *resourcev1.ResourceSlice {
	node := nodeName
	return &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:   DriverName,
			NodeName: &node,
			Pool: resourcev1.ResourcePool{
				Name:               nodeName,
				Generation:         1,
				ResourceSliceCount: int64(total),
			},
		},
	}
}

func budgetCounters(budget mig.Budget) map[string]resourcev1.Counter {
	return map[string]resourcev1.Counter{
		counterMemory: {Value: budget.Memory.DeepCopy()},
		counterSlices: {Value: *resource.NewQuantity(int64(budget.Slices), resource.DecimalSI)},
	}
}

// migDevice builds one MIG instance as a DRA device.
//
// Topology attributes are inherited from the physical GPU the instance is
// carved from, so a topology-aware selector keeps working under MIG. The uuid
// is likewise the physical GPU's, which is what lets a consumer group instances
// back onto the card they share — migInstanceID distinguishes them within it,
// mirroring how DCGM reports GPU_UUID alongside GPU_I_ID.
func migDevice(
	pool *v1alpha1.GPUPool,
	model *v1alpha1.GPUModel,
	instance mig.Instance,
	nodeName string,
) resourcev1.Device {
	attrs := map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		AttrProductName:   stringAttr(model.Spec.ProductName),
		AttrVendor:        stringAttr(model.Spec.Vendor),
		AttrUUID:          stringAttr(DeviceUUID(nodeName, instance.GPUIndex)),
		AttrMIGProfile:    stringAttr(instance.Profile.Name),
		AttrMIGInstanceID: intAttr(int64(instance.InstanceID)),
	}

	if domain := NVLinkDomain(instance.GPUIndex, pool.Spec.Topology.NVLinkDomainSize); domain != "" {
		attrs[AttrNVLinkDomain] = stringAttr(domain)
	}
	if pool.Spec.Topology.NUMAAware {
		attrs[AttrNUMANode] = intAttr(NUMANode(instance.GPUIndex, pool.Spec.Topology.NVLinkDomainSize))
	}

	return resourcev1.Device{
		Name:       instance.DeviceName,
		Attributes: attrs,
		Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
			// The instance's own framebuffer, not the whole card's.
			counterMemory: {Value: instance.Profile.Memory.DeepCopy()},
		},
		// Drawing on the physical GPU's budget is what makes overlapping
		// profiles mutually exclusive. The upstream scheduler enforces it;
		// ghostgpu contributes no allocation logic.
		ConsumesCounters: []resourcev1.DeviceCounterConsumption{{
			CounterSet: instance.CounterSet,
			Counters: map[string]resourcev1.Counter{
				counterMemory: {Value: instance.Profile.Memory.DeepCopy()},
				counterSlices: {Value: *resource.NewQuantity(int64(instance.Profile.Slices), resource.DecimalSI)},
			},
		}},
	}
}
