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

// Package mig models NVIDIA Multi-Instance GPU partitioning.
//
// A MIG-capable GPU is divided into instances drawn from two budgets: compute
// slices and framebuffer memory. Profiles overlap, so a physical GPU can offer
// many of them but only satisfy a subset simultaneously. DRA expresses this
// natively with sharedCounters and consumesCounters, and the upstream scheduler
// enforces the exclusivity — ghostgpu contributes no allocation logic.
package mig

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Profile is one MIG instance shape and what it draws from its GPU's budget.
type Profile struct {
	// Name as NVIDIA reports it, e.g. "1g.10gb". This becomes part of both the
	// DRA device name and, under the mixed strategy, an extended resource name.
	Name string

	// Memory this instance reserves from the GPU's framebuffer.
	Memory resource.Quantity

	// Slices is the number of compute slices consumed.
	Slices int32
}

// Budget is the total a single physical GPU can hand out.
type Budget struct {
	Memory resource.Quantity
	Slices int32
}

// Table is a product's full MIG capability.
type Table struct {
	Budget   Budget
	Profiles []Profile
}

// DeepCopy returns an independent copy. Callers apply user overrides on top of
// a built-in table, and handing back the package-level value would corrupt it
// for every subsequent caller.
func (t Table) DeepCopy() Table {
	out := Table{
		Budget:   Budget{Memory: t.Budget.Memory.DeepCopy(), Slices: t.Budget.Slices},
		Profiles: make([]Profile, len(t.Profiles)),
	}
	for i, p := range t.Profiles {
		out.Profiles[i] = Profile{Name: p.Name, Memory: p.Memory.DeepCopy(), Slices: p.Slices}
	}
	return out
}

// Built-in table keys, and the tokens used to match a product string to one.
const (
	tableA10040 = "a100-40gb"
	tableA10080 = "a100-80gb"
	tableH10080 = "h100-80gb"

	familyA100 = "a100"
	familyH100 = "h100"
	capacity40 = "40gb"
	capacity80 = "80gb"
)

func profile(name string, memory string, slices int32) Profile {
	return Profile{Name: name, Memory: resource.MustParse(memory), Slices: slices}
}

// builtInTables holds the profile sets NVIDIA publishes for MIG-capable
// hardware. Design spec §5 places these in the "faithful" fidelity tier: real
// tooling reasons about these exact names and shapes, so they are part of the
// contract rather than a convenience, and must not be invented.
//
// Memory figures follow the profile labels rather than the driver's exact
// framebuffer arithmetic. What matters for simulation is that the numbers are
// self-consistent — profiles must fit their budget, and the largest must
// consume all of it — which the tests assert.
//
// Profiles are ordered smallest to largest.
var builtInTables = map[string]Table{
	tableA10040: {
		Budget: Budget{Memory: resource.MustParse("40Gi"), Slices: 7},
		Profiles: []Profile{
			profile("1g.5gb", "5Gi", 1),
			profile("2g.10gb", "10Gi", 2),
			profile("3g.20gb", "20Gi", 3),
			profile("4g.20gb", "20Gi", 4),
			profile("7g.40gb", "40Gi", 7),
		},
	},
	tableA10080: {
		Budget: Budget{Memory: resource.MustParse("80Gi"), Slices: 7},
		Profiles: []Profile{
			profile("1g.10gb", "10Gi", 1),
			profile("2g.20gb", "20Gi", 2),
			profile("3g.40gb", "40Gi", 3),
			profile("4g.40gb", "40Gi", 4),
			profile("7g.80gb", "80Gi", 7),
		},
	},
	tableH10080: {
		Budget: Budget{Memory: resource.MustParse("80Gi"), Slices: 7},
		Profiles: []Profile{
			// The H100 adds a second single-slice profile with double the
			// memory, which is why memory and slices must be tracked as
			// separate counters rather than one derived from the other.
			profile("1g.10gb", "10Gi", 1),
			profile("1g.20gb", "20Gi", 1),
			profile("2g.20gb", "20Gi", 2),
			profile("3g.40gb", "40Gi", 3),
			profile("4g.40gb", "40Gi", 4),
			profile("7g.80gb", "80Gi", 7),
		},
	},
}

// productMatcher maps a normalized product string to a built-in table key.
// Order matters: the first match wins, so capacity-qualified entries must
// precede their bare family, or an 80GB A100 would resolve to the 40GB table.
var productMatchers = []struct {
	contains []string
	table    string
}{
	{contains: []string{familyA100, capacity40}, table: tableA10040},
	{contains: []string{familyA100, capacity80}, table: tableA10080},
	{contains: []string{familyA100}, table: tableA10080},
	{contains: []string{familyH100}, table: tableH10080},
}

// normalize lowercases and strips separators so that "NVIDIA-A100-SXM4-40GB",
// "NVIDIA A100 PCIe 40GB", and "nvidia_a100_40gb" all match alike.
func normalize(product string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(product) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ProfilesFor returns the built-in MIG table for a GPU product.
//
// Unknown products return false rather than a guess. Fabricating a profile set
// for hardware ghostgpu does not know would simulate something that does not
// exist, so the user must supply one explicitly.
func ProfilesFor(productName string) (Table, bool) {
	normalized := normalize(productName)
	if normalized == "" {
		return Table{}, false
	}

	for _, m := range productMatchers {
		matched := true
		for _, needle := range m.contains {
			if !strings.Contains(normalized, needle) {
				matched = false
				break
			}
		}
		if matched {
			return builtInTables[m.table].DeepCopy(), true
		}
	}
	return Table{}, false
}
