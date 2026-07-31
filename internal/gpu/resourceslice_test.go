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

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

const (
	productH100  = "NVIDIA-H100-SXM"
	poolName     = "h100-pool"
	vendorNVIDIA = "nvidia"
)

func testModel() *v1alpha1.GPUModel {
	return &v1alpha1.GPUModel{
		ObjectMeta: metav1.ObjectMeta{Name: "h100"},
		Spec: v1alpha1.GPUModelSpec{
			Vendor:            vendorNVIDIA,
			ProductName:       productH100,
			Memory:            resource.MustParse("80Gi"),
			ComputeCapability: "9.0",
		},
	}
}

func testPool(gpusPerNode, nvlinkDomainSize int32, numaAware bool) *v1alpha1.GPUPool {
	return &v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef:    "h100",
			GPUsPerNode: gpusPerNode,
			Topology: v1alpha1.TopologySpec{
				NVLinkDomainSize: nvlinkDomainSize,
				NUMAAware:        numaAware,
			},
		},
	}
}

func TestSliceName(t *testing.T) {
	if got := SliceName(poolName, nodeA); got != "h100-pool-node-a" {
		t.Errorf("SliceName = %q, want %q", got, "h100-pool-node-a")
	}
}

func TestBuildResourceSliceTopLevelShape(t *testing.T) {
	slice := BuildResourceSlice(testPool(8, 4, false), testModel(), nodeA, 0)

	if slice.Name != "h100-pool-node-a" {
		t.Errorf("Name = %q, want h100-pool-node-a", slice.Name)
	}
	if slice.Spec.Driver != DriverName {
		t.Errorf("Driver = %q, want %q", slice.Spec.Driver, DriverName)
	}
	if slice.Spec.NodeName == nil {
		t.Fatal("NodeName is nil; the slice would not be bound to a node")
	}
	if *slice.Spec.NodeName != nodeA {
		t.Errorf("NodeName = %q, want %q", *slice.Spec.NodeName, nodeA)
	}
	if slice.Spec.Pool.Name != nodeA {
		t.Errorf("Pool.Name = %q, want %q (one DRA pool per node)", slice.Spec.Pool.Name, nodeA)
	}
	if slice.Spec.Pool.ResourceSliceCount != 1 {
		t.Errorf("ResourceSliceCount = %d, want 1", slice.Spec.Pool.ResourceSliceCount)
	}
	if len(slice.Spec.Devices) != 8 {
		t.Fatalf("got %d devices, want 8", len(slice.Spec.Devices))
	}
}

func TestBuildResourceSliceDeviceNamesAreSequential(t *testing.T) {
	slice := BuildResourceSlice(testPool(4, 0, false), testModel(), nodeA, 0)
	want := []string{"gpu-0", "gpu-1", "gpu-2", "gpu-3"}
	for i, w := range want {
		if got := slice.Spec.Devices[i].Name; got != w {
			t.Errorf("device %d name = %q, want %q", i, got, w)
		}
	}
}

func TestBuildResourceSliceDeviceAttributes(t *testing.T) {
	slice := BuildResourceSlice(testPool(8, 4, true), testModel(), nodeA, 0)
	d := slice.Spec.Devices[5]

	cases := map[resourcev1.QualifiedName]string{
		AttrProductName:  productH100,
		AttrVendor:       vendorNVIDIA,
		AttrUUID:         DeviceUUID(nodeA, 5),
		AttrNVLinkDomain: domain1, // index 5, domain size 4
	}
	for name, want := range cases {
		attr, ok := d.Attributes[name]
		if !ok {
			t.Errorf("attribute %q missing", name)
			continue
		}
		if attr.StringValue == nil {
			t.Errorf("attribute %q has no string value", name)
			continue
		}
		if *attr.StringValue != want {
			t.Errorf("attribute %q = %q, want %q", name, *attr.StringValue, want)
		}
	}
}

func TestBuildResourceSliceMemoryCapacity(t *testing.T) {
	slice := BuildResourceSlice(testPool(2, 0, false), testModel(), nodeA, 0)
	mem, ok := slice.Spec.Devices[0].Capacity["memory"]
	if !ok {
		t.Fatal("memory capacity missing")
	}
	if mem.Value.Cmp(resource.MustParse("80Gi")) != 0 {
		t.Errorf("memory = %v, want 80Gi", mem.Value.String())
	}
}

func TestBuildResourceSliceOmitsNVLinkDomainWhenDisabled(t *testing.T) {
	slice := BuildResourceSlice(testPool(4, 0, false), testModel(), nodeA, 0)
	if _, ok := slice.Spec.Devices[0].Attributes[AttrNVLinkDomain]; ok {
		t.Error("nvlinkDomain present, want omitted when nvlinkDomainSize is 0")
	}
}

func TestBuildResourceSliceNUMAAttribute(t *testing.T) {
	t.Run("emitted when numaAware", func(t *testing.T) {
		slice := BuildResourceSlice(testPool(8, 4, true), testModel(), nodeA, 0)
		attr, ok := slice.Spec.Devices[4].Attributes[AttrNUMANode]
		if !ok {
			t.Fatal("numaNode attribute missing")
		}
		if attr.IntValue == nil {
			t.Fatal("numaNode is not an int attribute")
		}
		if *attr.IntValue != 1 {
			t.Errorf("numaNode = %d, want 1", *attr.IntValue)
		}
	})

	t.Run("omitted when not numaAware", func(t *testing.T) {
		slice := BuildResourceSlice(testPool(8, 4, false), testModel(), nodeA, 0)
		if _, ok := slice.Spec.Devices[0].Attributes[AttrNUMANode]; ok {
			t.Error("numaNode present, want omitted when numaAware is false")
		}
	})
}

// A restarted operator must republish byte-identical slices rather than
// churning them, which at fleet scale would be a significant write load.
func TestBuildResourceSliceIsDeterministic(t *testing.T) {
	a := BuildResourceSlice(testPool(8, 4, true), testModel(), nodeA, 0)
	b := BuildResourceSlice(testPool(8, 4, true), testModel(), nodeA, 0)

	if len(a.Spec.Devices) != len(b.Spec.Devices) {
		t.Fatalf("device count differs: %d vs %d", len(a.Spec.Devices), len(b.Spec.Devices))
	}
	for i := range a.Spec.Devices {
		av := *a.Spec.Devices[i].Attributes[AttrUUID].StringValue
		bv := *b.Spec.Devices[i].Attributes[AttrUUID].StringValue
		if av != bv {
			t.Errorf("device %d uuid differs between builds: %q vs %q", i, av, bv)
		}
	}
}

// Different nodes must not produce colliding device identities, or per-GPU
// metrics across a fleet would be ambiguous.
func TestBuildResourceSliceDiffersPerNode(t *testing.T) {
	a := BuildResourceSlice(testPool(2, 0, false), testModel(), nodeA, 0)
	b := BuildResourceSlice(testPool(2, 0, false), testModel(), nodeB, 0)

	if a.Name == b.Name {
		t.Errorf("slice names collide across nodes: %q", a.Name)
	}
	au := *a.Spec.Devices[0].Attributes[AttrUUID].StringValue
	bu := *b.Spec.Devices[0].Attributes[AttrUUID].StringValue
	if au == bu {
		t.Errorf("device UUIDs collide across nodes: %q", au)
	}
}

// The spike established a hard DRA limit of 128 devices per ResourceSlice.
// GPUPool validation caps gpusPerNode at 128, so the maximum must fit.
func TestBuildResourceSliceAtMaxDeviceCount(t *testing.T) {
	slice := BuildResourceSlice(testPool(128, 8, true), testModel(), nodeA, 0)
	if len(slice.Spec.Devices) != 128 {
		t.Fatalf("got %d devices, want 128", len(slice.Spec.Devices))
	}
	if len(slice.Spec.Devices) > 128 {
		t.Error("exceeds the DRA per-slice device limit")
	}
}
