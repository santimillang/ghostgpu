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
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

func validateResourceName(name string) []string {
	return validation.IsQualifiedName(name)
}

func migPool(t *testing.T, gpus int32) *v1alpha1.GPUPool {
	t.Helper()
	pool := testPool(gpus, 4, true)
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	return pool
}

// These counts are what real NVIDIA hardware reports. Both budgets bind and the
// tighter one wins: 1g.20gb is capped at four by memory even though seven would
// fit by compute slices.
func TestMaxInstancesMatchesRealHardware(t *testing.T) {
	table := h100Table(t)

	want := map[string]int64{
		"1g.10gb":       7,
		"1g.20gb":       4,
		"2g.20gb":       3,
		"3g.40gb":       2,
		"4g.40gb":       1,
		profileWholeGPU: 1,
	}

	for _, profile := range table.Profiles {
		expected, ok := want[profile.Name]
		if !ok {
			t.Errorf("unexpected profile %q in the H100 table", profile.Name)
			continue
		}
		if got := MaxInstances(profile, table.Budget); got != expected {
			t.Errorf("MaxInstances(%s) = %d, want %d", profile.Name, got, expected)
		}
	}
}

func TestMaxInstancesDegenerateProfiles(t *testing.T) {
	table := h100Table(t)

	if got := MaxInstances(mig.Profile{Name: "bad", Slices: 0}, table.Budget); got != 0 {
		t.Errorf("a profile consuming no slices yielded %d instances, want 0", got)
	}
	zeroMemory := mig.Profile{Name: "zero", Slices: 1}
	if got := MaxInstances(zeroMemory, table.Budget); got != 0 {
		t.Errorf("a profile consuming no memory yielded %d instances, want 0", got)
	}
}

func TestNodeResourcesWholeGPUs(t *testing.T) {
	got := NodeResources(testPool(8, 4, true), mig.Table{})

	if len(got) != 1 {
		t.Fatalf("got %d resources, want just nvidia.com/gpu: %v", len(got), got)
	}
	if q := got[GPUResourceName]; q.Value() != 8 {
		t.Errorf("nvidia.com/gpu = %d, want 8", q.Value())
	}
}

func TestNodeResourcesMixedStrategy(t *testing.T) {
	table := h100Table(t)
	got := NodeResources(migPool(t, 8), table)

	// Zero rather than absent: an explicit zero says the resource exists and is
	// exhausted, where an absent key is indistinguishable from "not yet updated".
	whole, ok := got[GPUResourceName]
	if !ok {
		t.Fatal("nvidia.com/gpu absent; a node leaving whole-GPU mode must say it has none")
	}
	if whole.Value() != 0 {
		t.Errorf("nvidia.com/gpu = %d, want 0 under MIG", whole.Value())
	}

	want := map[string]int64{
		"nvidia.com/mig-1g.10gb": 56, // 7 per GPU x 8
		"nvidia.com/mig-1g.20gb": 32,
		"nvidia.com/mig-2g.20gb": 24,
		"nvidia.com/mig-3g.40gb": 16,
		"nvidia.com/mig-4g.40gb": 8,
		"nvidia.com/mig-7g.80gb": 8,
	}
	for name, expected := range want {
		q, ok := got[MIGResourceName(name[len(MIGResourcePrefix):])]
		if !ok {
			t.Errorf("resource %s missing", name)
			continue
		}
		if q.Value() != expected {
			t.Errorf("%s = %d, want %d", name, q.Value(), expected)
		}
	}

	if len(got) != len(want)+1 {
		t.Errorf("got %d resources, want %d", len(got), len(want)+1)
	}
}

// A profile that cannot fit even once is omitted rather than advertised as
// zero. An explicit zero would claim the profile exists on this hardware, and
// a selector for it would match a node that can never satisfy it.
func TestNodeResourcesOmitsUnsatisfiableProfiles(t *testing.T) {
	table := h100Table(t)
	table.Profiles = append(table.Profiles, mig.Profile{
		Name:   "99g.999gb",
		Memory: resource.MustParse("999Gi"),
		Slices: 99,
	})

	got := NodeResources(migPool(t, 8), table)

	if _, present := got[MIGResourceName("99g.999gb")]; present {
		t.Error("a profile that cannot fit on the hardware was advertised")
	}
	if _, present := got[MIGResourceName("1g.10gb")]; !present {
		t.Error("satisfiable profiles should still be advertised")
	}
}

// The point of the whole feature: under a declared partition the advertised
// counts sum to something the hardware can satisfy, so a scheduler filling the
// node cannot overcommit it.
func TestNodeResourcesUnderPartitionAreSatisfiable(t *testing.T) {
	table := h100Table(t)
	pool := migPool(t, 8)
	pool.Spec.MIGPartition = []v1alpha1.MIGPartitionEntry{
		{Profile: "3g.40gb", Count: 1},
		{Profile: "1g.10gb", Count: 4},
	}

	got := NodeResources(pool, table)

	if q := got[MIGResourceName("3g.40gb")]; q.Value() != 8 {
		t.Errorf("mig-3g.40gb = %d, want 8 (1 per GPU x 8)", q.Value())
	}
	if q := got[MIGResourceName("1g.10gb")]; q.Value() != 32 {
		t.Errorf("mig-1g.10gb = %d, want 32 (4 per GPU x 8)", q.Value())
	}

	// Profiles not in the partition do not exist on this hardware right now.
	for _, absent := range []string{profileWholeGPU, "1g.20gb", "2g.20gb", "4g.40gb"} {
		if q, present := got[MIGResourceName(absent)]; present {
			t.Errorf("mig-%s advertised as %d, but the partition does not create it",
				absent, q.Value())
		}
	}

	// The sum has to fit one card's budget, which is what makes it exact.
	var slices int32
	usedMemory := resource.NewQuantity(0, resource.BinarySI)
	for _, p := range table.Profiles {
		q := got[MIGResourceName(p.Name)]
		perGPU := int32(q.Value() / 8)
		slices += p.Slices * perGPU
		for range perGPU {
			usedMemory.Add(p.Memory)
		}
	}
	if slices > table.Budget.Slices {
		t.Errorf("advertised instances consume %d slices, more than the %d a GPU has",
			slices, table.Budget.Slices)
	}
	if usedMemory.Cmp(table.Budget.Memory) > 0 {
		t.Errorf("advertised instances consume %s, more than the %s a GPU has",
			usedMemory.String(), table.Budget.Memory.String())
	}
}

// The contrast that motivates the feature. Without a partition the counts are
// alternatives, and their sum is not satisfiable — deliberately, and recorded
// in the design spec's approximated tier.
func TestNodeResourcesWithoutPartitionOversubscribe(t *testing.T) {
	table := h100Table(t)
	got := NodeResources(migPool(t, 1), table)

	var slices int32
	for _, p := range table.Profiles {
		q := got[MIGResourceName(p.Name)]
		slices += p.Slices * int32(q.Value())
	}

	if slices <= table.Budget.Slices {
		t.Errorf("expected the dynamic projection to oversubscribe (%d slices vs %d);"+
			" if this now fits, the approximated-tier note is stale",
			slices, table.Budget.Slices)
	}
}

func TestNodeResourcesScaleWithGPUCount(t *testing.T) {
	table := h100Table(t)

	small := MIGResourceName("1g.10gb")

	one := NodeResources(migPool(t, 1), table)[small]
	if one.Value() != 7 {
		t.Errorf("1 GPU: 1g.10gb = %d, want 7", one.Value())
	}

	four := NodeResources(migPool(t, 4), table)[small]
	if four.Value() != 28 {
		t.Errorf("4 GPUs: 1g.10gb = %d, want 28", four.Value())
	}
}

// Resource names are what a pod requests. An invalid one would be rejected when
// the node is patched, failing the whole pool.
func TestNodeResourceNamesAreValid(t *testing.T) {
	for name := range NodeResources(migPool(t, 8), h100Table(t)) {
		if errs := validateResourceName(string(name)); len(errs) > 0 {
			t.Errorf("resource name %q invalid: %v", name, errs)
		}
	}
}
