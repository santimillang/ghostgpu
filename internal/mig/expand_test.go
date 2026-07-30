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
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

// DRA device names must be DNS labels, so the dot in a profile name cannot
// survive. The mapping has to be lossless in practice: two profiles must never
// collapse onto one device name.
func TestDeviceName(t *testing.T) {
	cases := map[string]struct {
		gpu     int32
		profile string
		want    string
	}{
		"first gpu":  {gpu: 0, profile: "1g.10gb", want: "gpu-0-1g-10gb"},
		"later gpu":  {gpu: 7, profile: "7g.80gb", want: "gpu-7-7g-80gb"},
		"a100 small": {gpu: 3, profile: "1g.5gb", want: "gpu-3-1g-5gb"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DeviceName(tc.gpu, tc.profile); got != tc.want {
				t.Errorf("DeviceName(%d, %q) = %q, want %q", tc.gpu, tc.profile, got, tc.want)
			}
		})
	}
}

// A ResourceSlice with an invalid device name is rejected wholesale, so every
// profile ghostgpu ships must produce a valid one.
func TestDeviceNamesAreValidDNSLabels(t *testing.T) {
	for product, table := range builtInTables {
		for _, p := range table.Profiles {
			name := DeviceName(0, p.Name)
			for _, msg := range validation.IsDNS1123Label(name) {
				t.Errorf("%s/%s produced invalid device name %q: %s", product, p.Name, name, msg)
			}
		}
	}
}

// Two different profiles on the same GPU must never collide, or one would
// silently overwrite the other in the published slice.
func TestDeviceNamesAreUniqueAcrossProfiles(t *testing.T) {
	for product, table := range builtInTables {
		seen := make(map[string]string, len(table.Profiles))
		for _, p := range table.Profiles {
			name := DeviceName(0, p.Name)
			if other, dup := seen[name]; dup {
				t.Errorf("%s: profiles %q and %q both map to device name %q",
					product, other, p.Name, name)
			}
			seen[name] = p.Name
		}
	}
}

func TestCounterSetName(t *testing.T) {
	for gpu, want := range map[int32]string{0: "gpu-0", 7: "gpu-7", 127: "gpu-127"} {
		if got := CounterSetName(gpu); got != want {
			t.Errorf("CounterSetName(%d) = %q, want %q", gpu, got, want)
		}
	}
}

// Counter set names live in the same pool-wide namespace across every slice, so
// they must be unique per physical GPU.
func TestCounterSetNamesAreUniquePerGPU(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for gpu := range int32(128) {
		name := CounterSetName(gpu)
		if _, dup := seen[name]; dup {
			t.Fatalf("counter set name %q repeats at gpu %d", name, gpu)
		}
		seen[name] = struct{}{}
	}
}

// GPU_I_ID is what a real driver assigns to a MIG instance and what DCGM
// reports per instance. The v0.3 exporter keys metrics on it, so it must be
// stable and unique within a GPU.
func TestInstanceIDIsStableAndUniqueWithinAGPU(t *testing.T) {
	// Checked across every built-in table, not just one. The ID is derived by
	// hashing into a bounded range, so a collision is possible in principle and
	// would silently merge two instances' metrics in the v0.3 exporter.
	for product, table := range builtInTables {
		t.Run(product, func(t *testing.T) {
			seen := make(map[int32]string, len(table.Profiles))
			for _, p := range table.Profiles {
				id := InstanceID(0, p.Name)
				if other, dup := seen[id]; dup {
					t.Errorf("profiles %q and %q share instance ID %d", other, p.Name, id)
				}
				seen[id] = p.Name

				if again := InstanceID(0, p.Name); again != id {
					t.Errorf("InstanceID is not stable for %q: %d then %d", p.Name, id, again)
				}
			}
		})
	}
}

// Real GPU_I_ID values are scoped to their GPU: instance 0 exists on every
// physical GPU. Uniqueness is a within-GPU property, and the device name is
// what distinguishes instances across GPUs.
func TestInstanceIDRepeatsAcrossGPUs(t *testing.T) {
	if InstanceID(0, p1g10gb) != InstanceID(3, p1g10gb) {
		t.Error("instance IDs differ across GPUs; they are scoped to their GPU on real hardware")
	}
}

func TestExpandProducesEveryProfileOnEveryGPU(t *testing.T) {
	table, ok := ProfilesFor(productH100)
	if !ok {
		t.Fatal("no H100 table")
	}

	instances := Expand(4, table)

	want := 4 * len(table.Profiles)
	if len(instances) != want {
		t.Fatalf("got %d instances, want %d (4 GPUs x %d profiles)",
			len(instances), want, len(table.Profiles))
	}

	names := make(map[string]struct{}, len(instances))
	for _, in := range instances {
		if _, dup := names[in.DeviceName]; dup {
			t.Errorf("duplicate device name %q", in.DeviceName)
		}
		names[in.DeviceName] = struct{}{}
	}
}

// Expansion feeds slice construction, which must be a pure function so a
// restarted operator republishes identical slices rather than churning them.
func TestExpandIsDeterministic(t *testing.T) {
	table, _ := ProfilesFor(productH100)

	first := Expand(8, table)
	second := Expand(8, table)

	if len(first) != len(second) {
		t.Fatalf("length differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].DeviceName != second[i].DeviceName {
			t.Errorf("instance %d differs: %q vs %q", i, first[i].DeviceName, second[i].DeviceName)
		}
	}
}

// Grouping matters for sharding: instances are emitted GPU by GPU, so a shard
// boundary falling mid-GPU is a deliberate, testable situation rather than an
// accident of ordering.
func TestExpandGroupsByGPU(t *testing.T) {
	table, _ := ProfilesFor(productH100)
	n := int32(len(table.Profiles))

	instances := Expand(3, table)

	for i, in := range instances {
		wantGPU := int32(i) / n
		if in.GPUIndex != wantGPU {
			t.Errorf("instance %d has GPUIndex %d, want %d", i, in.GPUIndex, wantGPU)
		}
		if in.CounterSet != CounterSetName(wantGPU) {
			t.Errorf("instance %d references counter set %q, want %q",
				i, in.CounterSet, CounterSetName(wantGPU))
		}
	}
}

func TestExpandCarriesProfileConsumption(t *testing.T) {
	table, _ := ProfilesFor(productH100)
	instances := Expand(1, table)

	for i, in := range instances {
		want := table.Profiles[i]
		if in.Profile.Name != want.Name {
			t.Errorf("instance %d profile = %q, want %q", i, in.Profile.Name, want.Name)
		}
		if in.Profile.Slices != want.Slices {
			t.Errorf("instance %d slices = %d, want %d", i, in.Profile.Slices, want.Slices)
		}
		if in.Profile.Memory.Cmp(want.Memory) != 0 {
			t.Errorf("instance %d memory = %v, want %v",
				i, in.Profile.Memory.String(), want.Memory.String())
		}
	}
}

func TestExpandZeroGPUs(t *testing.T) {
	table, _ := ProfilesFor(productH100)
	if got := Expand(0, table); len(got) != 0 {
		t.Errorf("got %d instances for 0 GPUs, want none", len(got))
	}
}
