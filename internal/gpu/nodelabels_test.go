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
)

func TestNodeLabels(t *testing.T) {
	got := NodeLabels(testPool(8, 4, true), testModel())

	want := map[string]string{
		"nvidia.com/gpu.present":       "true",
		"nvidia.com/gpu.count":         "8",
		"nvidia.com/gpu.product":       productH100,
		"nvidia.com/gpu.memory":        "81920",
		"nvidia.com/gpu.compute.major": "9",
		"nvidia.com/gpu.compute.minor": "0",
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

			if got := NodeLabels(testPool(1, 0, false), model)["nvidia.com/gpu.memory"]; got != tc.want {
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

func TestNodeLabelsCountTracksPool(t *testing.T) {
	for _, n := range []int32{1, 4, 128} {
		pool := testPool(n, 0, false)
		want := map[int32]string{1: "1", 4: "4", 128: "128"}[n]

		if got := NodeLabels(pool, testModel())["nvidia.com/gpu.count"]; got != want {
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
