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
	"strconv"
	"strings"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// mib is the divisor turning a resource.Quantity's byte count into the MiB
// integer that GPU Feature Discovery reports.
const mib = 1024 * 1024

// labelTrue is the value GFD uses for its boolean labels. Kubernetes label
// values are strings, so this is the literal "true", not a formatted bool.
const labelTrue = "true"

// GPU Feature Discovery label keys.
//
// Real tooling selects on these literal strings, so they are an external
// contract: renaming one makes ghostgpu's nodes invisible to the software it
// exists to test. TestLabelKeysMatchGFD pins each to its literal value so the
// constants cannot drift from what GFD actually emits.
const (
	LabelGPUPresent   = "nvidia.com/gpu.present"
	LabelGPUCount     = "nvidia.com/gpu.count"
	LabelGPUProduct   = "nvidia.com/gpu.product"
	LabelGPUMemory    = "nvidia.com/gpu.memory"
	LabelComputeMajor = "nvidia.com/gpu.compute.major"
	LabelComputeMinor = "nvidia.com/gpu.compute.minor"
	LabelMIGCapable   = "nvidia.com/mig.capable"
	LabelMIGStrategy  = "nvidia.com/mig.strategy"
)

// NodeLabels returns GPU Feature Discovery-compatible labels for a simulated
// node.
//
// Key names and value formats mirror NVIDIA GFD exactly. This is deliberate and
// load-bearing: real tooling — HAMi, Kueue node selectors, scheduling plugins,
// dashboards — selects on these literal keys, so any divergence would make
// ghostgpu's nodes silently invisible to the very software it exists to test.
// Treat the key set as an external contract, not an implementation detail.
func NodeLabels(pool *v1alpha1.GPUPool, model *v1alpha1.GPUModel) map[string]string {
	labels := map[string]string{
		LabelGPUPresent: labelTrue,
		LabelGPUCount:   strconv.Itoa(int(pool.Spec.GPUsPerNode)),
		LabelGPUProduct: model.Spec.ProductName,
		// GFD reports memory as a bare integer count of MiB, with no unit
		// suffix. Truncating division matches it for sub-MiB remainders.
		LabelGPUMemory: strconv.FormatInt(model.Spec.Memory.Value()/mib, 10),
	}

	// The CRD schema constrains computeCapability to "<major>.<minor>", but an
	// unvalidated struct can still reach here. Omit the labels rather than
	// emitting empty values, which are valid selector targets and would match.
	if major, minor, ok := strings.Cut(model.Spec.ComputeCapability, "."); ok {
		labels[LabelComputeMajor] = major
		labels[LabelComputeMinor] = minor
	}

	if pool.Spec.MIGEnabled() {
		labels[LabelMIGCapable] = labelTrue
		labels[LabelMIGStrategy] = MIGStrategyMixed
		// Under the mixed strategy gpu.count reports GPUs that are *not*
		// partitioned, and ghostgpu partitions every one of them. Leaving the
		// physical count here would tell a selector there are whole cards to
		// schedule onto when none are allocatable.
		labels[LabelGPUCount] = "0"
	}

	return labels
}

// MIGStrategyMixed is NVIDIA's strategy for exposing MIG instances as one
// extended resource per profile, as opposed to "single", which requires every
// instance on a node to share one profile.
const MIGStrategyMixed = "mixed"
