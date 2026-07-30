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
)

// The one place the GFD label keys are written as literals.
//
// Everywhere else refers to the constants, which keeps them from drifting
// apart; this test is what stops them drifting away from GFD itself. Renaming
// a key here should feel like editing an external API, because it is one:
// real tooling selects on these exact strings.
func TestLabelKeysMatchGFD(t *testing.T) {
	want := map[string]string{
		LabelGPUPresent:   "nvidia.com/gpu.present",
		LabelGPUCount:     "nvidia.com/gpu.count",
		LabelGPUProduct:   "nvidia.com/gpu.product",
		LabelGPUMemory:    "nvidia.com/gpu.memory",
		LabelComputeMajor: "nvidia.com/gpu.compute.major",
		LabelComputeMinor: "nvidia.com/gpu.compute.minor",
		LabelMIGCapable:   "nvidia.com/mig.capable",
		LabelMIGStrategy:  "nvidia.com/mig.strategy",
	}

	if len(want) != 8 {
		t.Fatalf("expected 8 distinct label keys, got %d; two constants share a value", len(want))
	}
	for constant, literal := range want {
		if constant != literal {
			t.Errorf("label constant = %q, want %q", constant, literal)
		}
	}

	if MIGStrategyMixed != "mixed" {
		t.Errorf("MIGStrategyMixed = %q, want mixed", MIGStrategyMixed)
	}
	if MIGResourcePrefix != "nvidia.com/mig-" {
		t.Errorf("MIGResourcePrefix = %q, want nvidia.com/mig-", MIGResourcePrefix)
	}
	if GPUResourceName != "nvidia.com/gpu" {
		t.Errorf("GPUResourceName = %q, want nvidia.com/gpu", GPUResourceName)
	}
}

func TestNodeLabels(t *testing.T) {
	got := NodeLabels(testPool(8, 4, true), testModel())

	want := map[string]string{
		LabelGPUPresent:   labelTrue,
		LabelGPUCount:     "8",
		LabelGPUProduct:   productH100,
		LabelGPUMemory:    "81920",
		LabelComputeMajor: "9",
		LabelComputeMinor: "0",
	}

	for k, w := range want {
		if got[k] != w {
			t.Errorf("label %s = %q, want %q", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d labels, want %d: %v", len(got), len(want), got)
	}
}

// GFD reports memory as a bare integer count of MiB — no unit suffix and no
// SI/binary ambiguity. Selectors in the wild compare these lexically as well
// as numerically, so the exact rendering matters.
func TestNodeLabelsMemoryIsBareMiB(t *testing.T) {
	cases := []struct {
		quantity string
		want     string
	}{
		{"80Gi", "81920"},
		{"40Gi", "40960"},
		{"24Gi", "24576"},
		{"16000Mi", "16000"},
		// Decimal gigabytes round down to whole MiB rather than erroring.
		{"80G", "76293"},
	}

	for _, tc := range cases {
		t.Run(tc.quantity, func(t *testing.T) {
			model := testModel()
			model.Spec.Memory = resource.MustParse(tc.quantity)

			if got := NodeLabels(testPool(1, 0, false), model)[LabelGPUMemory]; got != tc.want {
				t.Errorf("gpu.memory = %q, want %q", got, tc.want)
			}
		})
	}
}

// The CRD schema requires computeCapability to match "<major>.<minor>", but
// NodeLabels is also called from tests and future callers holding an unvalidated
// struct. It must degrade by omitting the labels, never by emitting empty ones:
// an empty label value is a valid selector target and would match wrongly.
func TestNodeLabelsOmitsComputeCapabilityWhenUnset(t *testing.T) {
	model := testModel()
	model.Spec.ComputeCapability = ""

	got := NodeLabels(testPool(2, 0, false), model)

	for _, k := range []string{"nvidia.com/gpu.compute.major", "nvidia.com/gpu.compute.minor"} {
		if v, ok := got[k]; ok {
			t.Errorf("label %s present with value %q, want omitted", k, v)
		}
	}
}

// Under NVIDIA's mixed strategy gpu.count reports GPUs that are *not*
// partitioned. ghostgpu partitions every one of them, so leaving the physical
// count would tell a node selector there are whole cards to schedule onto when
// none are allocatable.
func TestNodeLabelsUnderMIG(t *testing.T) {
	pool := testPool(8, 4, true)
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG

	got := NodeLabels(pool, testModel())

	want := map[string]string{
		LabelMIGCapable:  labelTrue,
		LabelMIGStrategy: "mixed",
		LabelGPUCount:    "0",
		// Identity labels still apply: the card is the same card.
		LabelGPUPresent: labelTrue,
		LabelGPUProduct: productH100,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("label %s = %q, want %q", k, got[k], w)
		}
	}
}

func TestNodeLabelsOmitsMIGLabelsWhenNotPartitioned(t *testing.T) {
	got := NodeLabels(testPool(8, 4, true), testModel())

	for _, k := range []string{"nvidia.com/mig.capable", "nvidia.com/mig.strategy"} {
		if v, ok := got[k]; ok {
			t.Errorf("label %s present with value %q on a non-MIG pool", k, v)
		}
	}
	if got[LabelGPUCount] != "8" {
		t.Errorf("gpu.count = %q, want 8 when GPUs are whole", got[LabelGPUCount])
	}
}

func TestNodeLabelsCountTracksPool(t *testing.T) {
	for _, n := range []int32{1, 4, 128} {
		pool := testPool(n, 0, false)
		want := map[int32]string{1: "1", 4: "4", 128: "128"}[n]

		if got := NodeLabels(pool, testModel())[LabelGPUCount]; got != want {
			t.Errorf("gpusPerNode %d: gpu.count = %q, want %q", n, got, want)
		}
	}
}

// Every emitted label must be a valid Kubernetes label key and value, or the
// node patch is rejected at admission for the whole pool.
func TestNodeLabelsAreValidKubernetesLabels(t *testing.T) {
	for k, v := range NodeLabels(testPool(8, 4, true), testModel()) {
		for _, msg := range validation.IsQualifiedName(k) {
			t.Errorf("label key %q invalid: %s", k, msg)
		}
		for _, msg := range validation.IsValidLabelValue(v) {
			t.Errorf("label %q value %q invalid: %s", k, v, msg)
		}
	}
}
