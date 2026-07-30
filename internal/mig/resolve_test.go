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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

func model(product, memory string) *v1alpha1.GPUModel {
	return &v1alpha1.GPUModel{
		ObjectMeta: metav1.ObjectMeta{Name: "m"},
		Spec: v1alpha1.GPUModelSpec{
			Vendor:            "nvidia",
			ProductName:       product,
			Memory:            resource.MustParse(memory),
			ComputeCapability: "9.0",
		},
	}
}

func TestResolveUsesBuiltInTable(t *testing.T) {
	table, err := Resolve(model(productH100, "80Gi"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(table.Profiles) != 6 {
		t.Errorf("got %d profiles, want 6", len(table.Profiles))
	}
	if table.Budget.Slices != 7 {
		t.Errorf("budget slices = %d, want 7", table.Budget.Slices)
	}
}

// The budget's memory comes from the model, not the table. An H100 NVL shares
// the H100 profile family but carries 94GB; inheriting the table's 80GB would
// simulate a card the user did not ask for.
func TestResolveTakesBudgetMemoryFromTheModel(t *testing.T) {
	m := model("NVIDIA-H100-NVL", "94Gi")
	m.Spec.MIGProfiles = []v1alpha1.MIGProfileSpec{
		{Name: p1g10gb, Memory: resource.MustParse("10Gi"), Slices: 1},
		{Name: p7g80gb, Memory: resource.MustParse("94Gi"), Slices: 7},
	}

	table, err := Resolve(m)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if table.Budget.Memory.Cmp(resource.MustParse("94Gi")) != 0 {
		t.Errorf("budget memory = %v, want 94Gi", table.Budget.Memory.String())
	}
}

func TestResolveUserProfilesOverrideBuiltIns(t *testing.T) {
	m := model(productH100, "80Gi")
	m.Spec.MIGProfiles = []v1alpha1.MIGProfileSpec{
		{Name: p1g10gb, Memory: resource.MustParse("10Gi"), Slices: 1},
	}

	table, err := Resolve(m)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(table.Profiles) != 1 {
		t.Fatalf("got %d profiles, want the 1 the user declared", len(table.Profiles))
	}
	if table.Profiles[0].Name != p1g10gb {
		t.Errorf("profile = %q, want 1g.10gb", table.Profiles[0].Name)
	}
}

func TestResolveExplicitBudgetWins(t *testing.T) {
	m := model(productH100, "80Gi")
	m.Spec.MIGBudget = &v1alpha1.MIGBudgetSpec{
		Memory: ptr.To(resource.MustParse("40Gi")),
		Slices: 4,
	}
	m.Spec.MIGProfiles = []v1alpha1.MIGProfileSpec{
		{Name: p4g40gb, Memory: resource.MustParse("40Gi"), Slices: 4},
	}

	table, err := Resolve(m)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if table.Budget.Slices != 4 {
		t.Errorf("budget slices = %d, want 4", table.Budget.Slices)
	}
	if table.Budget.Memory.Cmp(resource.MustParse("40Gi")) != 0 {
		t.Errorf("budget memory = %v, want 40Gi", table.Budget.Memory.String())
	}
}

// An unknown GPU with no declared profiles cannot be partitioned. Silently
// producing an empty table would publish a MIG pool with no devices at all.
func TestResolveRejectsUnknownProductWithoutProfiles(t *testing.T) {
	_, err := Resolve(model("NVIDIA-GTX-1080", "8Gi"))
	if err == nil {
		t.Fatal("expected an error for an unknown product with no profiles")
	}
	if !strings.Contains(err.Error(), "migProfiles") {
		t.Errorf("error should point the user at migProfiles, got: %v", err)
	}
}

// A profile that cannot fit its own budget would be published as a device that
// can never be allocated.
func TestResolveRejectsProfilesExceedingBudget(t *testing.T) {
	cases := map[string]v1alpha1.MIGProfileSpec{
		"too many slices": {Name: "9g.80gb", Memory: resource.MustParse("80Gi"), Slices: 9},
		"too much memory": {Name: "1g.99gb", Memory: resource.MustParse("99Gi"), Slices: 1},
	}

	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			m := model(productH100, "80Gi")
			m.Spec.MIGProfiles = []v1alpha1.MIGProfileSpec{bad}

			if _, err := Resolve(m); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestResolveRejectsDuplicateProfileNames(t *testing.T) {
	m := model(productH100, "80Gi")
	m.Spec.MIGProfiles = []v1alpha1.MIGProfileSpec{
		{Name: p1g10gb, Memory: resource.MustParse("10Gi"), Slices: 1},
		{Name: p1g10gb, Memory: resource.MustParse("20Gi"), Slices: 1},
	}

	if _, err := Resolve(m); err == nil {
		t.Error("expected an error for duplicate profile names")
	}
}

// Resolve feeds slice construction, which must be a pure function so a
// restarted operator republishes rather than churns.
func TestResolveIsDeterministic(t *testing.T) {
	m := model("NVIDIA-A100-SXM4-40GB", "40Gi")

	first, err := Resolve(m)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(m)
	if err != nil {
		t.Fatal(err)
	}

	if len(first.Profiles) != len(second.Profiles) {
		t.Fatalf("profile count differs: %d vs %d", len(first.Profiles), len(second.Profiles))
	}
	for i := range first.Profiles {
		if first.Profiles[i].Name != second.Profiles[i].Name {
			t.Errorf("profile %d differs: %q vs %q", i, first.Profiles[i].Name, second.Profiles[i].Name)
		}
	}
}

// Resolve must not write through to the model it was handed.
func TestResolveDoesNotMutateModel(t *testing.T) {
	m := model(productH100, "80Gi")
	before := len(m.Spec.MIGProfiles)

	if _, err := Resolve(m); err != nil {
		t.Fatal(err)
	}
	if len(m.Spec.MIGProfiles) != before {
		t.Errorf("Resolve wrote %d profiles back onto the model", len(m.Spec.MIGProfiles))
	}
}
