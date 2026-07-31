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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// rackLabel is the topology key these fixtures fragment a fleet along.
const rackLabel = "rack"

func occupancyNode(labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: labels}}
}

func occupancyPool(entries ...v1alpha1.OccupancyEntry) *v1alpha1.GPUPool {
	return &v1alpha1.GPUPool{
		Spec: v1alpha1.GPUPoolSpec{GPUsPerNode: 8, Occupancy: entries},
	}
}

func TestBusyGPUsWithoutOccupancyIsZero(t *testing.T) {
	if got := BusyGPUs(occupancyPool(), occupancyNode(nil)); got != 0 {
		t.Errorf("BusyGPUs = %d, want 0 — an empty fleet is the default", got)
	}
}

// The whole point of a list is a fleet that is unevenly full: seven GPUs free
// but spread 2/2/2/1 is the scenario the feature exists for, and one number
// per pool cannot express it.
func TestBusyGPUsVariesByNode(t *testing.T) {
	pool := occupancyPool(
		v1alpha1.OccupancyEntry{NodeSelector: map[string]string{rackLabel: "a"}, BusyPerNode: 6},
		v1alpha1.OccupancyEntry{NodeSelector: map[string]string{rackLabel: "b"}, BusyPerNode: 7},
	)

	for _, tc := range []struct {
		labels map[string]string
		want   int32
	}{
		{map[string]string{rackLabel: "a"}, 6},
		{map[string]string{rackLabel: "b"}, 7},
		{map[string]string{rackLabel: "c"}, 0},
		{nil, 0},
	} {
		if got := BusyGPUs(pool, occupancyNode(tc.labels)); got != tc.want {
			t.Errorf("BusyGPUs(%v) = %d, want %d", tc.labels, got, tc.want)
		}
	}
}

// First match wins, so ordering is meaningful and a trailing selector-less
// entry reads as a default.
func TestBusyGPUsFirstMatchWins(t *testing.T) {
	pool := occupancyPool(
		v1alpha1.OccupancyEntry{NodeSelector: map[string]string{rackLabel: "a"}, BusyPerNode: 2},
		v1alpha1.OccupancyEntry{BusyPerNode: 5},
	)

	if got := BusyGPUs(pool, occupancyNode(map[string]string{rackLabel: "a"})); got != 2 {
		t.Errorf("BusyGPUs = %d, want 2 — the specific entry precedes the default", got)
	}
	if got := BusyGPUs(pool, occupancyNode(map[string]string{rackLabel: "z"})); got != 5 {
		t.Errorf("BusyGPUs = %d, want 5 from the selector-less default", got)
	}
}

// A selector matches when every one of its labels matches, not merely one.
func TestBusyGPUsRequiresEveryLabel(t *testing.T) {
	pool := occupancyPool(v1alpha1.OccupancyEntry{
		NodeSelector: map[string]string{rackLabel: "a", "zone": "us-east-1a"},
		BusyPerNode:  4,
	})

	if got := BusyGPUs(pool, occupancyNode(map[string]string{rackLabel: "a"})); got != 0 {
		t.Errorf("BusyGPUs = %d, want 0 — only one of two selector labels matched", got)
	}
	if got := BusyGPUs(pool, occupancyNode(map[string]string{
		rackLabel: "a", "zone": "us-east-1a", "extra": "ignored",
	})); got != 4 {
		t.Errorf("BusyGPUs = %d, want 4 — every selector label matched", got)
	}
}

// Occupancy larger than the node has is clamped rather than producing taints
// for devices that do not exist. The controller rejects it on status too; this
// keeps the pure function total.
func TestBusyGPUsClampsToGPUsPerNode(t *testing.T) {
	pool := occupancyPool(v1alpha1.OccupancyEntry{BusyPerNode: 99})
	if got := BusyGPUs(pool, occupancyNode(nil)); got != 8 {
		t.Errorf("BusyGPUs = %d, want it clamped to gpusPerNode 8", got)
	}
}

func TestValidateOccupancy(t *testing.T) {
	pool := occupancyPool(v1alpha1.OccupancyEntry{BusyPerNode: 9})
	if err := ValidateOccupancy(pool); err == nil {
		t.Fatal("want an error when busyPerNode exceeds gpusPerNode")
	}

	ok := occupancyPool(v1alpha1.OccupancyEntry{BusyPerNode: 8})
	if err := ValidateOccupancy(ok); err != nil {
		t.Errorf("occupying every GPU is legal, got %v", err)
	}

	negative := occupancyPool(v1alpha1.OccupancyEntry{BusyPerNode: -1})
	if err := ValidateOccupancy(negative); err == nil {
		t.Error("want an error for negative occupancy")
	}
}

// The taint is what the scheduler acts on, so its key and effect are an
// external contract: a consumer writing a toleration for it depends on both.
func TestOccupiedTaintShape(t *testing.T) {
	taint := OccupiedTaint()

	if taint.Key != "ghostgpu.dev/occupied" {
		t.Errorf("taint key = %q, want ghostgpu.dev/occupied", taint.Key)
	}
	if taint.Effect != "NoSchedule" {
		t.Errorf("taint effect = %q, want NoSchedule", taint.Effect)
	}
}

// Occupied devices are still published. Removing them would change the fleet's
// size, and the whole point is a full cluster rather than a smaller one.
func TestBuildResourceSliceTaintsOccupiedDevices(t *testing.T) {
	pool := occupancyPool()
	pool.Spec.GPUsPerNode = 4
	model := &v1alpha1.GPUModel{Spec: v1alpha1.GPUModelSpec{ProductName: "H100", Vendor: "nvidia"}}

	slice := BuildResourceSlice(pool, model, "node-a", 3)

	if len(slice.Spec.Devices) != 4 {
		t.Fatalf("published %d devices, want all 4 — occupancy hides devices, it does not delete them",
			len(slice.Spec.Devices))
	}
	for i, device := range slice.Spec.Devices {
		tainted := len(device.Taints) == 1 && device.Taints[0].Key == OccupiedTaintKey
		if want := i < 3; tainted != want {
			t.Errorf("device %d (%s) tainted = %v, want %v", i, device.Name, tainted, want)
		}
	}
}

// Under MIG the unit of occupancy is still the card. Tainting one instance
// would leave a larger overlapping profile on the same silicon allocatable, so
// the card would not be occupied at all.
func TestBuildMIGSlicesTaintsEveryInstanceOfAnOccupiedGPU(t *testing.T) {
	pool := occupancyPool()
	pool.Spec.GPUsPerNode = 3
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	model := &v1alpha1.GPUModel{
		Spec: v1alpha1.GPUModelSpec{
			ProductName: "NVIDIA-H100-80GB-HBM3",
			Vendor:      "nvidia",
			Memory:      resource.MustParse("80Gi"),
		},
	}
	table, err := mig.Resolve(model)
	if err != nil {
		t.Fatalf("mig.Resolve: %v", err)
	}

	perGPU := map[int]struct{ tainted, total int }{}
	for _, slice := range BuildMIGSlices(pool, model, table, "node-a", 1) {
		for _, device := range slice.Spec.Devices {
			// Device names are gpu-<index>-<profile>, so the index is the card.
			index := int(device.Name[len("gpu-")] - '0')
			seen := perGPU[index]
			seen.total++
			if len(device.Taints) > 0 {
				seen.tainted++
			}
			perGPU[index] = seen
		}
	}

	if len(perGPU) != 3 {
		t.Fatalf("saw %d GPUs, want 3", len(perGPU))
	}
	if got := perGPU[0]; got.tainted != got.total || got.total == 0 {
		t.Errorf("gpu-0: %d of %d instances tainted, want every one", got.tainted, got.total)
	}
	for _, index := range []int{1, 2} {
		if got := perGPU[index]; got.tainted != 0 {
			t.Errorf("gpu-%d: %d instances tainted, want none", index, got.tainted)
		}
	}
}

// The legacy path expresses occupancy as allocatable below capacity, which is
// what the distinction already means in Kubernetes rather than a trick.
func TestNodeAllocatableSubtractsOccupancy(t *testing.T) {
	pool := occupancyPool()
	pool.Spec.GPUsPerNode = 8

	capacity := NodeResources(pool, mig.Table{})
	allocatable := NodeAllocatable(pool, mig.Table{}, 6)

	if got := capacity[GPUResourceName]; got.Value() != 8 {
		t.Errorf("capacity = %d, want the hardware's full 8", got.Value())
	}
	if got := allocatable[GPUResourceName]; got.Value() != 2 {
		t.Errorf("allocatable = %d, want 2 free", got.Value())
	}
}

// A fully occupied node must still advertise the resource at zero. A key that
// vanishes from allocatable reads as "not updated yet" rather than "exhausted".
func TestNodeAllocatableKeepsResourcesAtZero(t *testing.T) {
	pool := occupancyPool()
	pool.Spec.GPUsPerNode = 2
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	pool.Spec.MIGPartition = []v1alpha1.MIGPartitionEntry{{Profile: profileSmallest, Count: 4}}

	model := &v1alpha1.GPUModel{
		Spec: v1alpha1.GPUModelSpec{
			ProductName: "NVIDIA-H100-80GB-HBM3",
			Memory:      resource.MustParse("80Gi"),
		},
	}
	table, err := mig.Resolve(model)
	if err != nil {
		t.Fatalf("mig.Resolve: %v", err)
	}

	capacity := NodeResources(pool, table)
	allocatable := NodeAllocatable(pool, table, 2)

	name := MIGResourceName(profileSmallest)
	if got, ok := capacity[name]; !ok || got.Value() != 8 {
		t.Errorf("capacity[%s] = %v (present %v), want 8", name, got.Value(), ok)
	}
	got, ok := allocatable[name]
	if !ok {
		t.Fatalf("allocatable dropped %s entirely; that reads as 'not updated yet'", name)
	}
	if got.Value() != 0 {
		t.Errorf("allocatable[%s] = %d, want 0 with every card occupied", name, got.Value())
	}
}
