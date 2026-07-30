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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Profile names the tests assert on. They are NVIDIA's, not ours, so pinning
// them in one place keeps every expectation referring to the same literal.
const (
	p1g10gb = "1g.10gb"
	p4g40gb = "4g.40gb"
	p7g80gb = "7g.80gb"

	productH100 = "NVIDIA-H100-80GB-HBM3"
)

func TestProfilesForKnownProducts(t *testing.T) {
	cases := []struct {
		product      string
		wantProfiles []string
		wantSlices   int32
		wantMemory   string
	}{
		{
			product:      "NVIDIA-A100-SXM4-40GB",
			wantProfiles: []string{"1g.5gb", "2g.10gb", "3g.20gb", "4g.20gb", "7g.40gb"},
			wantSlices:   7,
			wantMemory:   "40Gi",
		},
		{
			product:      "NVIDIA-A100-SXM4-80GB",
			wantProfiles: []string{p1g10gb, "2g.20gb", "3g.40gb", p4g40gb, p7g80gb},
			wantSlices:   7,
			wantMemory:   "80Gi",
		},
		{
			product:      productH100,
			wantProfiles: []string{p1g10gb, "1g.20gb", "2g.20gb", "3g.40gb", p4g40gb, p7g80gb},
			wantSlices:   7,
			wantMemory:   "80Gi",
		},
	}

	for _, tc := range cases {
		t.Run(tc.product, func(t *testing.T) {
			table, ok := ProfilesFor(tc.product)
			if !ok {
				t.Fatalf("no built-in table for %q", tc.product)
			}

			got := make([]string, 0, len(table.Profiles))
			for _, p := range table.Profiles {
				got = append(got, p.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.wantProfiles, ",") {
				t.Errorf("profiles = %v, want %v", got, tc.wantProfiles)
			}
			if table.Budget.Slices != tc.wantSlices {
				t.Errorf("budget slices = %d, want %d", table.Budget.Slices, tc.wantSlices)
			}
			if table.Budget.Memory.Cmp(resource.MustParse(tc.wantMemory)) != 0 {
				t.Errorf("budget memory = %v, want %v", table.Budget.Memory.String(), tc.wantMemory)
			}
		})
	}
}

// GFD product strings vary by form factor and vendor casing. Matching must be
// tolerant enough that a user does not have to hand-write a profile table for a
// GPU ghostgpu already knows.
func TestProfilesForIsTolerantOfProductNaming(t *testing.T) {
	for _, product := range []string{
		"NVIDIA-H100-SXM",
		"nvidia-h100-pcie",
		"NVIDIA H100 80GB HBM3",
		"NVIDIA-A100-PCIE-40GB",
	} {
		t.Run(product, func(t *testing.T) {
			if _, ok := ProfilesFor(product); !ok {
				t.Errorf("no table matched %q", product)
			}
		})
	}
}

// A 40GB A100 and an 80GB A100 have different profiles. Falling back to the
// wrong one would silently simulate hardware the user did not ask for.
func TestProfilesForDistinguishesA100Capacities(t *testing.T) {
	small, ok := ProfilesFor("NVIDIA-A100-SXM4-40GB")
	if !ok {
		t.Fatal("no table for A100 40GB")
	}
	large, ok := ProfilesFor("NVIDIA-A100-SXM4-80GB")
	if !ok {
		t.Fatal("no table for A100 80GB")
	}

	if small.Profiles[0].Name == large.Profiles[0].Name {
		t.Errorf("40GB and 80GB A100 resolved to the same table (%s)", small.Profiles[0].Name)
	}
	if small.Budget.Memory.Cmp(large.Budget.Memory) == 0 {
		t.Error("40GB and 80GB A100 have the same memory budget")
	}
}

func TestProfilesForUnknownProduct(t *testing.T) {
	for _, product := range []string{"NVIDIA-GTX-1080", "", "some-accelerator"} {
		if _, ok := ProfilesFor(product); ok {
			t.Errorf("ProfilesFor(%q) returned a table; unknown products must require explicit profiles", product)
		}
	}
}

// Every built-in profile must be satisfiable on its own hardware. A profile
// consuming more than the budget could never be allocated, and the operator
// would publish a device that is permanently unschedulable.
func TestBuiltInProfilesFitTheirBudget(t *testing.T) {
	for product, table := range builtInTables {
		for _, p := range table.Profiles {
			if p.Slices > table.Budget.Slices {
				t.Errorf("%s/%s consumes %d slices, budget is %d",
					product, p.Name, p.Slices, table.Budget.Slices)
			}
			if p.Memory.Cmp(table.Budget.Memory) > 0 {
				t.Errorf("%s/%s consumes %v memory, budget is %v",
					product, p.Name, p.Memory.String(), table.Budget.Memory.String())
			}
			if p.Slices < 1 {
				t.Errorf("%s/%s consumes %d slices; a profile must consume at least one",
					product, p.Name, p.Slices)
			}
		}
	}
}

// Profile names become part of an extended resource name under the mixed
// strategy (nvidia.com/mig-1g.10gb). An invalid name would be rejected at
// admission when the node is patched, failing the whole pool.
func TestBuiltInProfileNamesAreValidResourceNames(t *testing.T) {
	for product, table := range builtInTables {
		for _, p := range table.Profiles {
			for _, msg := range validation.IsQualifiedName("nvidia.com/mig-" + p.Name) {
				t.Errorf("%s/%s yields an invalid resource name: %s", product, p.Name, msg)
			}
		}
	}
}

// The largest profile is the whole GPU. If it did not consume the entire
// budget, two of them could coexist on one physical GPU.
func TestLargestProfileConsumesWholeBudget(t *testing.T) {
	for product, table := range builtInTables {
		largest := table.Profiles[len(table.Profiles)-1]
		if largest.Slices != table.Budget.Slices {
			t.Errorf("%s: largest profile %s consumes %d of %d slices",
				product, largest.Name, largest.Slices, table.Budget.Slices)
		}
		if largest.Memory.Cmp(table.Budget.Memory) != 0 {
			t.Errorf("%s: largest profile %s consumes %v of %v memory",
				product, largest.Name, largest.Memory.String(), table.Budget.Memory.String())
		}
	}
}

func TestProfilesAreOrderedBySize(t *testing.T) {
	for product, table := range builtInTables {
		for i := 1; i < len(table.Profiles); i++ {
			if table.Profiles[i].Slices < table.Profiles[i-1].Slices {
				t.Errorf("%s: profiles not ordered by size at %d (%s before %s)",
					product, i, table.Profiles[i-1].Name, table.Profiles[i].Name)
			}
		}
	}
}

// Callers mutate the returned table when applying user overrides. Handing back
// the package-level table would corrupt it for every subsequent caller.
func TestProfilesForReturnsIndependentCopies(t *testing.T) {
	first, _ := ProfilesFor(productH100)
	first.Profiles[0].Name = "mutated"
	first.Budget.Slices = 99

	second, _ := ProfilesFor(productH100)
	if second.Profiles[0].Name == "mutated" {
		t.Error("mutating a returned table changed the built-in table")
	}
	if second.Budget.Slices == 99 {
		t.Error("mutating a returned budget changed the built-in table")
	}
}
