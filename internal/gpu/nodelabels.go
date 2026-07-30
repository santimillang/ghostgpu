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
		"nvidia.com/gpu.present": "true",
		"nvidia.com/gpu.count":   strconv.Itoa(int(pool.Spec.GPUsPerNode)),
		"nvidia.com/gpu.product": model.Spec.ProductName,
		// GFD reports memory as a bare integer count of MiB, with no unit
		// suffix. Truncating division matches it for sub-MiB remainders.
		"nvidia.com/gpu.memory": strconv.FormatInt(model.Spec.Memory.Value()/mib, 10),
	}

	// The CRD schema constrains computeCapability to "<major>.<minor>", but an
	// unvalidated struct can still reach here. Omit the labels rather than
	// emitting empty values, which are valid selector targets and would match.
	if major, minor, ok := strings.Cut(model.Spec.ComputeCapability, "."); ok {
		labels["nvidia.com/gpu.compute.major"] = major
		labels["nvidia.com/gpu.compute.minor"] = minor
	}

	return labels
}
