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
)

// Instance is one MIG device to publish: a profile realised on a specific
// physical GPU.
type Instance struct {
	// GPUIndex is the physical GPU this instance is carved from.
	GPUIndex int32

	// DeviceName is the DRA device name, e.g. "gpu-0-1g-10gb".
	DeviceName string

	// CounterSet names the shared counter set this instance draws down. It is
	// the physical GPU's, which is what makes overlapping profiles on one GPU
	// mutually exclusive.
	CounterSet string

	// InstanceID is the GPU_I_ID a real driver would assign.
	InstanceID int32

	// Profile is the shape and consumption of this instance.
	Profile Profile
}

// DeviceName is the DRA device name for a profile on a physical GPU.
//
// DRA device names must be DNS labels, so the dot in a profile name becomes a
// dash. The mapping stays collision-free for real profile names because they
// differ before the dot as well as after it, which the tests assert across
// every built-in table rather than assuming.
func DeviceName(gpuIndex int32, profile string) string {
	return fmt.Sprintf("gpu-%d-%s", gpuIndex, strings.ReplaceAll(profile, ".", "-"))
}

// CounterSetName is the shared counter set representing one physical GPU's
// budget. Counter sets resolve pool-wide rather than per-slice, so this name
// must be unique across every slice in the pool.
func CounterSetName(gpuIndex int32) string {
	return fmt.Sprintf("gpu-%d", gpuIndex)
}

// InstanceID is the GPU_I_ID a real driver assigns to a MIG instance.
//
// Real hardware derives this from the profile's placement on the GPU's slice
// array. ghostgpu derives it from the profile's slice count instead, which
// preserves the properties consumers actually depend on — stable across
// restarts, unique within a GPU — without inventing a placement engine that
// nothing yet reads. The v0.3 DCGM exporter keys per-instance metrics on it.
//
// The GPU index is accepted but unused: real GPU_I_ID values are scoped to
// their GPU, so instance 0 exists on every one of them. Uniqueness is a
// within-GPU property, and the device name distinguishes instances across GPUs.
// The parameter stays in the signature because callers reason in terms of a
// specific GPU, and because that scoping is the thing worth documenting here.
func InstanceID(_ int32, profile string) int32 {
	// A simple stable hash over the profile name, bounded to the range real
	// GPU_I_ID values occupy.
	var sum int32
	for _, r := range profile {
		sum = sum*31 + r
	}
	if sum < 0 {
		sum = -sum
	}
	return sum % 100
}

// Expand realises every profile on every GPU of a node.
//
// Instances are emitted GPU by GPU, in profile order. That grouping is
// load-bearing for sharding: a device slice holds at most 64 counter-consuming
// devices, so a shard boundary can fall in the middle of a GPU. Because counter
// sets resolve pool-wide that is harmless, but the ordering makes it a
// deliberate, testable situation rather than an accident.
func Expand(gpusPerNode int32, table Table) []Instance {
	instances := make([]Instance, 0, gpusPerNode*int32(len(table.Profiles)))

	for gpu := range gpusPerNode {
		for _, p := range table.Profiles {
			instances = append(instances, Instance{
				GPUIndex:   gpu,
				DeviceName: DeviceName(gpu, p.Name),
				CounterSet: CounterSetName(gpu),
				InstanceID: InstanceID(gpu, p.Name),
				Profile:    Profile{Name: p.Name, Memory: p.Memory.DeepCopy(), Slices: p.Slices},
			})
		}
	}
	return instances
}
