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

package mig

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// A declared partition models *static* MIG: an administrator has already
// created these instances, and they are the only ones that exist. Dynamic MIG,
// where every profile is offered and the scheduler chooses, is what an empty
// partition means and what Expand produces.
//
// The distinction is what makes the legacy extended-resource path exact. Under
// a partition the per-profile counts sum to something the hardware can actually
// satisfy, because every declared instance coexists with the others by
// construction. Without one, the counts describe alternatives that a scalar
// resource cannot express as mutually exclusive.

// PartitionDeviceName is the DRA device name for one instance of a profile.
//
// The ordinal is what distinguishes two instances of the same profile on one
// card. Dynamic expansion has no ordinal because it publishes one device per
// profile representing a possibility rather than a created instance.
func PartitionDeviceName(gpuIndex int32, profile string, ordinal int32) string {
	return fmt.Sprintf("%s-%d", DeviceName(gpuIndex, profile), ordinal)
}

// ValidatePartition checks that every declared instance exists on this hardware
// and that they all fit one GPU at once.
//
// Fitting matters more here than it does for dynamic MIG. There, an
// over-large profile is merely unschedulable; here, the whole layout is a claim
// about what the card is carrying, and a claim the hardware could not honour
// would be advertised as satisfiable capacity.
func ValidatePartition(entries []v1alpha1.MIGPartitionEntry, table Table) error {
	if len(entries) == 0 {
		return nil
	}

	byName := make(map[string]Profile, len(table.Profiles))
	for _, p := range table.Profiles {
		byName[p.Name] = p
	}

	var usedSlices int32
	usedMemory := resource.NewQuantity(0, resource.BinarySI)

	for _, e := range entries {
		profile, ok := byName[e.Profile]
		if !ok {
			available := make([]string, 0, len(table.Profiles))
			for _, p := range table.Profiles {
				available = append(available, p.Name)
			}
			return fmt.Errorf("migPartition names profile %q, which this GPU does not support; available: %s",
				e.Profile, strings.Join(available, ", "))
		}

		usedSlices += profile.Slices * e.Count

		instanceMemory := profile.Memory.DeepCopy()
		for range e.Count {
			usedMemory.Add(instanceMemory)
		}
	}

	if usedSlices > table.Budget.Slices {
		return fmt.Errorf("migPartition consumes %d compute slices but the GPU has %d",
			usedSlices, table.Budget.Slices)
	}
	if usedMemory.Cmp(table.Budget.Memory) > 0 {
		return fmt.Errorf("migPartition consumes %s memory but the GPU has %s",
			usedMemory.String(), table.Budget.Memory.String())
	}
	return nil
}

// ExpandPartitioned realises a declared partition on every GPU of a node.
//
// Instances are emitted GPU by GPU, and within a GPU in the table's profile
// order rather than the order the entries were written, so the same layout
// always produces the same slices however the manifest was arranged.
func ExpandPartitioned(gpusPerNode int32, table Table, entries []v1alpha1.MIGPartitionEntry) []Instance {
	counts := make(map[string]int32, len(entries))
	total := int32(0)
	for _, e := range entries {
		counts[e.Profile] += e.Count
		total += e.Count
	}

	instances := make([]Instance, 0, gpusPerNode*total)

	for gpu := range gpusPerNode {
		// A running instance ID per GPU. Two instances of one profile are
		// different instances, so they cannot share the hash-derived ID that
		// dynamic expansion uses; the exporter would merge their metrics.
		var nextID int32

		for _, profile := range table.Profiles {
			for ordinal := range counts[profile.Name] {
				instances = append(instances, Instance{
					GPUIndex:   gpu,
					DeviceName: PartitionDeviceName(gpu, profile.Name, ordinal),
					CounterSet: CounterSetName(gpu),
					InstanceID: nextID,
					Profile: Profile{
						Name:   profile.Name,
						Memory: profile.Memory.DeepCopy(),
						Slices: profile.Slices,
					},
				})
				nextID++
			}
		}
	}

	return instances
}
