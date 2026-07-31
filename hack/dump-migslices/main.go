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

// Command dump-migslices prints the ResourceSlices ghostgpu would publish for a
// MIG pool, as a YAML stream.
//
// It exists so the generated shapes can be checked against a real API server
// without deploying the operator. Unit tests can assert the sharding
// arithmetic, but only an API server can confirm the objects are actually
// accepted — and every limit this layout works around was discovered that way.
package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
	"github.com/santimillang/ghostgpu/internal/mig"
)

func main() {
	gpus := flag.Int("gpus", 16, "simulated GPUs per node")
	product := flag.String("product", "NVIDIA-H100-80GB-HBM3", "GPU product name")
	memory := flag.String("memory", "80Gi", "memory per GPU")
	node := flag.String("node", "node-a", "node name")
	busy := flag.Int("busy", 0, "physical GPUs to mark occupied, so the device taints can be checked too")
	faulted := flag.Int("faulted", 0, "physical GPUs to mark failed, which taint with NoExecute")
	flag.Parse()

	model := &v1alpha1.GPUModel{
		ObjectMeta: metav1.ObjectMeta{Name: "h100"},
		Spec: v1alpha1.GPUModelSpec{
			Vendor:            "nvidia",
			ProductName:       *product,
			Memory:            resource.MustParse(*memory),
			ComputeCapability: "9.0",
		},
	}
	pool := &v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: "h100-pool"},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef:    "h100",
			GPUsPerNode: int32(*gpus),
			SharingMode: v1alpha1.SharingModeMIG,
			Topology:    v1alpha1.TopologySpec{NVLinkDomainSize: 4, NUMAAware: true},
		},
	}

	table, err := mig.Resolve(model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	state := gpu.NodeState{Busy: int32(*busy), Faulted: int32(*faulted), XID: 79}
	for i, slice := range gpu.BuildMIGSlices(pool, model, table, *node, state) {
		slice.APIVersion = "resource.k8s.io/v1"
		slice.Kind = "ResourceSlice"

		out, err := yaml.Marshal(slice)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if i > 0 {
			fmt.Println("---")
		}
		fmt.Print(string(out))
	}
}
