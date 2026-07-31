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

package metrics

import (
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
)

// Fixtures shared across the package's tests.
const (
	testPool  = "h100-pool"
	testModel = "h100"
	testNode  = "node-a"
	product   = "NVIDIA H100 80GB HBM3"

	testNamespace    = "team-a"
	defaultNamespace = "default"
	profileLarge     = "3g.40gb"
	firstDevice      = "gpu-0"
	testWorkload     = "trainer"

	// Label keys and values the per-workload utilization fixtures select on.
	jobLabel  = "job"
	tierLabel = "tier"
	tierBatch = "batch"
)

func testModels() map[string]*v1alpha1.GPUModel {
	return map[string]*v1alpha1.GPUModel{
		testModel: {
			ObjectMeta: metav1.ObjectMeta{Name: testModel},
			Spec: v1alpha1.GPUModelSpec{
				ProductName: product,
				Memory:      resource.MustParse("80Gi"),
			},
		},
	}
}

func testPools(utilization *v1alpha1.UtilizationSpec) []v1alpha1.GPUPool {
	return []v1alpha1.GPUPool{{
		ObjectMeta: metav1.ObjectMeta{Name: testPool},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef:    testModel,
			GPUsPerNode: 2,
			Utilization: utilization,
		},
	}}
}

func device(name string, attrs map[resourcev1.QualifiedName]resourcev1.DeviceAttribute) resourcev1.Device {
	return resourcev1.Device{
		Name:       name,
		Attributes: attrs,
		Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
			"memory": {Value: resource.MustParse("80Gi")},
		},
	}
}

func wholeGPU(index int32) resourcev1.Device {
	return device(gpu.DeviceName(testNode, index), map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		gpu.AttrUUID:        {StringValue: ptr.To(gpu.DeviceUUID(testNode, index))},
		gpu.AttrProductName: {StringValue: ptr.To(product)},
	})
}

func slice(devices ...resourcev1.Device) resourcev1.ResourceSlice {
	return resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:   testPool + "-" + testNode,
			Labels: map[string]string{PoolLabel: testPool},
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:  gpu.DriverName,
			Pool:    resourcev1.ResourcePool{Name: testNode},
			Devices: devices,
		},
	}
}

func TestCardsFromWholeGPUs(t *testing.T) {
	cards := CardsFrom(
		testPools(nil), testModels(),
		[]resourcev1.ResourceSlice{slice(wholeGPU(0), wholeGPU(1))},
		nil, nil,
	)

	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	if cards[0].Index != 0 || cards[1].Index != 1 {
		t.Errorf("indices = %d, %d, want 0 and 1", cards[0].Index, cards[1].Index)
	}
	if cards[0].Node != testNode || cards[0].ProductName != product {
		t.Errorf("card = %+v, want the node and product from the slice", cards[0])
	}
	if cards[0].MemoryMiB != h100MiB {
		t.Errorf("memory = %d MiB, want %d from the GPUModel", cards[0].MemoryMiB, h100MiB)
	}
	if cards[0].Reading.GPUUtil != 0 {
		t.Errorf("unheld card reports %d%% util, want idle", cards[0].Reading.GPUUtil)
	}
}

// The attribution that comes free from DRA: the scheduler already recorded
// which device went to which pod, so there is nothing to re-derive.
func TestCardsFromAttributesHolders(t *testing.T) {
	holders := map[Allocation]Holder{
		{Pool: testNode, Device: gpu.DeviceName(testNode, 1)}: {Namespace: testNamespace, Pod: testWorkload},
	}

	cards := CardsFrom(
		testPools(nil), testModels(),
		[]resourcev1.ResourceSlice{slice(wholeGPU(0), wholeGPU(1))},
		holders, nil,
	)

	if cards[0].Holder.Pod != "" {
		t.Errorf("card 0 has holder %+v, want none", cards[0].Holder)
	}
	if cards[1].Holder.Pod != testWorkload || cards[1].Holder.Namespace != testNamespace {
		t.Errorf("card 1 holder = %+v, want team-a/trainer", cards[1].Holder)
	}
	if cards[1].Reading.GPUUtil != 100 {
		t.Errorf("held card reports %d%% util, want busy", cards[1].Reading.GPUUtil)
	}
}

// A device ghostgpu declared occupied has no pod, but it is just as unavailable
// as an allocated one. Reporting it idle would contradict the fleet the user
// asked for — and idle-GPU reaping is exactly what these metrics get used for.
func TestCardsFromTreatsOccupiedAsBusy(t *testing.T) {
	occupiedGPU := wholeGPU(0)
	occupiedGPU.Taints = []resourcev1.DeviceTaint{gpu.OccupiedTaint()}

	cards := CardsFrom(
		testPools(nil), testModels(),
		[]resourcev1.ResourceSlice{slice(occupiedGPU)},
		nil, nil,
	)

	if cards[0].Reading.GPUUtil != 100 {
		t.Errorf("occupied card reports %d%% util, want busy", cards[0].Reading.GPUUtil)
	}
	if cards[0].Holder.Pod != "" {
		t.Errorf("occupied card names a holder %+v; there is no pod behind it", cards[0].Holder)
	}
}

func migInstance(index int32, profile string, instanceID int64, memory string) resourcev1.Device {
	d := device(gpu.DeviceName(testNode, index)+"-"+profile,
		map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			gpu.AttrUUID:          {StringValue: ptr.To(gpu.DeviceUUID(testNode, index))},
			gpu.AttrProductName:   {StringValue: ptr.To(product)},
			gpu.AttrMIGProfile:    {StringValue: ptr.To(profile)},
			gpu.AttrMIGInstanceID: {IntValue: ptr.To(instanceID)},
		})
	d.Capacity["memory"] = resourcev1.DeviceCapacity{Value: resource.MustParse(memory)}
	return d
}

// MIG instances arrive spread across several device slices, so the card they
// belong to has to be recovered from their names rather than assumed.
func TestCardsFromGroupsMIGInstancesOntoTheirCard(t *testing.T) {
	first := slice(migInstance(0, profileLarge, 3, "40Gi"))
	second := slice(migInstance(0, "1g.10gb", 7, "10Gi"), migInstance(1, "7g.80gb", 0, "80Gi"))
	second.Name += "-1"

	cards := CardsFrom(
		testPools(nil), testModels(),
		[]resourcev1.ResourceSlice{first, second},
		map[Allocation]Holder{
			{Pool: testNode, Device: "gpu-0-3g.40gb"}: {Namespace: defaultNamespace, Pod: testWorkload},
		},
		nil,
	)

	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2 physical GPUs", len(cards))
	}
	if len(cards[0].Instances) != 2 {
		t.Fatalf("card 0 has %d instances, want 2 gathered across both slices", len(cards[0].Instances))
	}

	busy := cards[0].Instances[0]
	if busy.Profile != profileLarge || busy.InstanceID != 3 {
		t.Errorf("instance = %+v, want 3g.40gb with GPU_I_ID 3", busy)
	}
	if busy.MemoryMiB != 40*1024 {
		t.Errorf("instance memory = %d MiB, want its own 40Gi", busy.MemoryMiB)
	}
	if busy.Holder.Pod != testWorkload {
		t.Errorf("instance holder = %+v, want trainer", busy.Holder)
	}

	// A card carrying one saturated instance is not idle hardware, so the
	// card-level reading takes the busiest instance rather than a mean.
	if cards[0].Reading.GPUUtil != 100 {
		t.Errorf("card util = %d, want the busiest instance's 100", cards[0].Reading.GPUUtil)
	}
	// 40Gi of the card's 80Gi is in use, so the card reports half full.
	if cards[0].Reading.FBUsedPercent != 50 {
		t.Errorf("card framebuffer = %d%%, want 50", cards[0].Reading.FBUsedPercent)
	}
}

// A slice from another driver, or one whose pool has been deleted, is not
// ghostgpu's to describe.
func TestCardsFromIgnoresForeignSlices(t *testing.T) {
	foreign := slice(wholeGPU(0))
	foreign.Labels = map[string]string{PoolLabel: "some-other-pool"}

	cards := CardsFrom(testPools(nil), testModels(), []resourcev1.ResourceSlice{foreign}, nil, nil)
	if len(cards) != 0 {
		t.Errorf("cards = %d, want none", len(cards))
	}
}

func TestHoldersSkipsOtherDriversAndUnreservedClaims(t *testing.T) {
	claims := []resourcev1.ResourceClaim{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "ours", Namespace: defaultNamespace},
			Status: resourcev1.ResourceClaimStatus{
				ReservedFor: []resourcev1.ResourceClaimConsumerReference{
					{Resource: podsResource, Name: "trainer"},
				},
				Allocation: &resourcev1.AllocationResult{
					Devices: resourcev1.DeviceAllocationResult{
						Results: []resourcev1.DeviceRequestAllocationResult{
							{Driver: gpu.DriverName, Pool: testNode, Device: firstDevice},
							{Driver: "gpu.nvidia.com", Pool: testNode, Device: "gpu-1"},
						},
					},
				},
			},
		},
		{
			// Allocated but not yet reserved: no workload to attribute it to.
			ObjectMeta: metav1.ObjectMeta{Name: "unreserved", Namespace: defaultNamespace},
			Status: resourcev1.ResourceClaimStatus{
				Allocation: &resourcev1.AllocationResult{
					Devices: resourcev1.DeviceAllocationResult{
						Results: []resourcev1.DeviceRequestAllocationResult{
							{Driver: gpu.DriverName, Pool: testNode, Device: "gpu-2"},
						},
					},
				},
			},
		},
	}

	held := Holders(claims)

	if len(held) != 1 {
		t.Fatalf("holders = %v, want only the ghostgpu device with a pod", held)
	}
	if got := held[Allocation{Pool: testNode, Device: firstDevice}]; got.Pod != "trainer" {
		t.Errorf("gpu-0 holder = %+v, want trainer", got)
	}
	// A real GPU driver's device must never gain a phantom holder from us.
	if _, ours := held[Allocation{Pool: testNode, Device: "gpu-1"}]; ours {
		t.Error("attributed a device belonging to another driver")
	}
}
