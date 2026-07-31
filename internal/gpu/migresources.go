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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// GPUResourceName is the extended resource advertising whole GPUs.
const GPUResourceName corev1.ResourceName = "nvidia.com/gpu"

// MIGResourcePrefix is the extended resource prefix NVIDIA's mixed strategy
// uses for MIG instances, e.g. nvidia.com/mig-1g.10gb.
const MIGResourcePrefix = "nvidia.com/mig-"

// MIGResourceName is the extended resource for one MIG profile.
func MIGResourceName(profile string) corev1.ResourceName {
	return corev1.ResourceName(MIGResourcePrefix + profile)
}

// MaxInstances is how many instances of a profile one physical GPU can hold.
//
// Both budgets bind, and the tighter one wins: an H100's 1g.20gb is limited to
// four by memory even though seven would fit by compute slices. Deriving it
// rather than tabulating it is what keeps the numbers correct for a GPU whose
// memory differs from the profile family's nominal capacity.
func MaxInstances(profile mig.Profile, budget mig.Budget) int64 {
	if profile.Slices <= 0 {
		return 0
	}
	bySlices := int64(budget.Slices / profile.Slices)

	byMemory := int64(0)
	if profileBytes := profile.Memory.Value(); profileBytes > 0 {
		byMemory = budget.Memory.Value() / profileBytes
	}

	return min(bySlices, byMemory)
}

// NodeResources is the extended-resource capacity a simulated node advertises.
//
// Under MIG this follows NVIDIA's "mixed" strategy: one resource per profile,
// with nvidia.com/gpu at zero because no whole card is separately allocatable
// once it is partitioned.
//
// A caveat worth stating plainly, because it is a property of the legacy path
// rather than of this implementation: scalar extended resources cannot express
// that profiles on one GPU are mutually exclusive. A node advertising both
// nvidia.com/mig-1g.10gb and nvidia.com/mig-7g.80gb will let a scheduler
// allocate from both at once, which real hardware could not satisfy. The DRA
// path models the exclusion correctly through shared counters; this projection
// exists for tooling that predates DRA, and the design spec records it as
// approximated rather than faithful.
func NodeResources(pool *v1alpha1.GPUPool, table mig.Table) corev1.ResourceList {
	return nodeResourcesFor(pool, table, pool.Spec.GPUsPerNode)
}

// NodeAllocatable is the capacity a simulated node offers the scheduler once
// declared occupancy is taken out.
//
// Expressed as allocatable below capacity rather than as reduced capacity,
// because that is already what the distinction means in Kubernetes: allocatable
// is the part of a node's capacity available to this scheduler, which is how
// --system-reserved describes memory held by something outside its view. A
// spike confirmed the scheduler honours it and ignores the larger capacity.
//
// Unlike the MIG projection, this is faithful rather than approximated: there
// is nothing about "six of these eight GPUs are already busy" that a scalar
// resource cannot say.
func NodeAllocatable(pool *v1alpha1.GPUPool, table mig.Table, busy int32) corev1.ResourceList {
	return nodeResourcesFor(pool, table, max(0, pool.Spec.GPUsPerNode-busy))
}

func nodeResourcesFor(pool *v1alpha1.GPUPool, table mig.Table, gpusAvailable int32) corev1.ResourceList {
	gpus := int64(gpusAvailable)

	if !pool.Spec.MIGEnabled() {
		return corev1.ResourceList{
			GPUResourceName: *resource.NewQuantity(gpus, resource.DecimalSI),
		}
	}

	// Zero rather than absent: an explicit zero tells a scheduler the resource
	// exists and is exhausted, where an absent key on a node that previously
	// advertised whole GPUs is indistinguishable from "not updated yet".
	resources := corev1.ResourceList{
		GPUResourceName: *resource.NewQuantity(0, resource.DecimalSI),
	}

	// A declared partition is the exact case. Every instance in it coexists, so
	// the per-profile counts sum to something the hardware can genuinely
	// satisfy and a scheduler cannot overcommit the node.
	declared := pool.Spec.MIGPartition
	perGPU := make(map[string]int32, len(declared))
	for _, e := range declared {
		perGPU[e.Profile] += e.Count
	}

	// Iterate the table, not the entries, so output order is stable.
	for _, profile := range table.Profiles {
		// Without a partition, each count is the most instances of that profile
		// the card could hold. Individually faithful; their sum overcommits,
		// because these are alternatives and a scalar resource cannot say so.
		// See issue #28.
		perCard := int64(perGPU[profile.Name])
		if len(declared) == 0 {
			perCard = MaxInstances(profile, table.Budget)
		}
		if perCard <= 0 {
			// The profile does not exist on this hardware at all, which is
			// different from existing and being unavailable.
			continue
		}
		// Keyed off perCard rather than the product, so that a node whose GPUs
		// are all occupied still advertises the profile at zero. The key set has
		// to match the one capacity uses: a resource that vanishes from
		// allocatable reads as "not updated yet" rather than "exhausted".
		resources[MIGResourceName(profile.Name)] = *resource.NewQuantity(perCard*gpus, resource.DecimalSI)
	}

	return resources
}
