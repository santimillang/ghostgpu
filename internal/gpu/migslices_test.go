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
	"fmt"
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

const (
	profileWholeGPU = "7g.80gb"
	profileThird    = "3g.40gb"
	profileSmallest = "1g.10gb"
)

func h100Table(t *testing.T) mig.Table {
	t.Helper()
	table, ok := mig.ProfilesFor("NVIDIA-H100-80GB-HBM3")
	if !ok {
		t.Fatal("no built-in H100 table")
	}
	return table
}

// counterSlices returns the slices carrying sharedCounters, deviceSlices the
// rest. The API rejects a slice holding both, which the v0.1 spike measured.
func partition(slices []*resourcev1.ResourceSlice) (counters, devices []*resourcev1.ResourceSlice) {
	for _, s := range slices {
		if len(s.Spec.SharedCounters) > 0 {
			counters = append(counters, s)
		} else {
			devices = append(devices, s)
		}
	}
	return counters, devices
}

// The sharding formula from the spike:
//
//	counterSlices = ceil(gpus / 8)      // 8 sharedCounters per slice
//	deviceSlices  = ceil(gpus * profiles / 64)
func TestBuildMIGSlicesShardingBoundaries(t *testing.T) {
	table := h100Table(t)
	profiles := int32(len(table.Profiles)) // 6 for the H100

	cases := []struct {
		gpus          int32
		wantCounters  int
		wantDevices   int
		wantDeviceSum int
	}{
		// One counter slice, one device slice.
		{gpus: 8, wantCounters: 1, wantDevices: 1, wantDeviceSum: 48},
		// Two counter slices: the 8-counter limit binds before the device
		// limit does. This is the case an 8-GPU development node never reaches.
		{gpus: 16, wantCounters: 2, wantDevices: 2, wantDeviceSum: 96},
		// A single GPU still needs one of each.
		{gpus: 1, wantCounters: 1, wantDevices: 1, wantDeviceSum: 6},
		// Exactly on the counter boundary.
		{gpus: 9, wantCounters: 2, wantDevices: 1, wantDeviceSum: 54},
		// The CRD maximum.
		{gpus: 128, wantCounters: 16, wantDevices: 12, wantDeviceSum: 768},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d gpus", tc.gpus), func(t *testing.T) {
			pool := testPool(tc.gpus, 4, true)
			slices := BuildMIGSlices(pool, testModel(), table, nodeA, NodeState{})

			counters, devices := partition(slices)

			if len(counters) != tc.wantCounters {
				t.Errorf("got %d counter slices, want %d", len(counters), tc.wantCounters)
			}
			if len(devices) != tc.wantDevices {
				t.Errorf("got %d device slices, want %d", len(devices), tc.wantDevices)
			}

			total := 0
			for _, s := range devices {
				total += len(s.Spec.Devices)
			}
			if total != tc.wantDeviceSum {
				t.Errorf("got %d devices total, want %d (%d gpus x %d profiles)",
					total, tc.wantDeviceSum, tc.gpus, profiles)
			}
		})
	}
}

// Both limits are hard API constraints measured against a live API server.
// Exceeding either is rejected outright, taking the whole node's simulation
// with it.
func TestBuildMIGSlicesRespectsAPILimits(t *testing.T) {
	table := h100Table(t)

	for _, gpus := range []int32{1, 8, 9, 16, 64, 128} {
		t.Run(fmt.Sprintf("%d gpus", gpus), func(t *testing.T) {
			slices := BuildMIGSlices(testPool(gpus, 4, true), testModel(), table, nodeA, NodeState{})

			for _, s := range slices {
				if len(s.Spec.SharedCounters) > MaxCounterSetsPerSlice {
					t.Errorf("slice %s has %d counter sets, limit is %d",
						s.Name, len(s.Spec.SharedCounters), MaxCounterSetsPerSlice)
				}
				if len(s.Spec.Devices) > MaxMIGDevicesPerSlice {
					t.Errorf("slice %s has %d devices, limit is %d",
						s.Name, len(s.Spec.Devices), MaxMIGDevicesPerSlice)
				}
				// The API rejects a slice carrying both.
				if len(s.Spec.SharedCounters) > 0 && len(s.Spec.Devices) > 0 {
					t.Errorf("slice %s carries both counters and devices", s.Name)
				}
			}
		})
	}
}

// Every slice belongs to one DRA pool, and resourceSliceCount must equal the
// number published or the scheduler waits forever for slices that never come.
func TestBuildMIGSlicesShareOnePoolWithCorrectCount(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(16, 4, true), testModel(), table, nodeA, NodeState{})

	for _, s := range slices {
		if s.Spec.Pool.Name != nodeA {
			t.Errorf("slice %s is in pool %q, want %q", s.Name, s.Spec.Pool.Name, nodeA)
		}
		if got := int(s.Spec.Pool.ResourceSliceCount); got != len(slices) {
			t.Errorf("slice %s declares resourceSliceCount %d, but %d slices were built",
				s.Name, got, len(slices))
		}
		if s.Spec.NodeName == nil || *s.Spec.NodeName != nodeA {
			t.Errorf("slice %s is not bound to node %s", s.Name, nodeA)
		}
		if s.Spec.Driver != DriverName {
			t.Errorf("slice %s has driver %q, want %q", s.Name, s.Spec.Driver, DriverName)
		}
	}
}

func TestBuildMIGSlicesNamesAreUnique(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(128, 8, true), testModel(), table, nodeA, NodeState{})

	seen := make(map[string]struct{}, len(slices))
	for _, s := range slices {
		if _, dup := seen[s.Name]; dup {
			t.Errorf("duplicate slice name %q", s.Name)
		}
		seen[s.Name] = struct{}{}
	}
}

// Every physical GPU needs exactly one counter set, carrying both budgets.
// A missing one would leave its instances unallocatable; a duplicate would be
// rejected, since counter set names resolve pool-wide.
func TestBuildMIGSlicesEmitOneCounterSetPerGPU(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(16, 4, true), testModel(), table, nodeA, NodeState{})
	counters, _ := partition(slices)

	seen := map[string]struct{}{}
	for _, s := range counters {
		for _, cs := range s.Spec.SharedCounters {
			if _, dup := seen[cs.Name]; dup {
				t.Errorf("counter set %q declared twice", cs.Name)
			}
			seen[cs.Name] = struct{}{}

			memory, ok := cs.Counters["memory"]
			if !ok {
				t.Errorf("counter set %q has no memory counter", cs.Name)
			} else if memory.Value.Cmp(table.Budget.Memory) != 0 {
				t.Errorf("counter set %q memory = %v, want %v",
					cs.Name, memory.Value.String(), table.Budget.Memory.String())
			}

			slicesCounter, ok := cs.Counters["slices"]
			if !ok {
				t.Errorf("counter set %q has no slices counter", cs.Name)
			} else if slicesCounter.Value.Value() != int64(table.Budget.Slices) {
				t.Errorf("counter set %q slices = %d, want %d",
					cs.Name, slicesCounter.Value.Value(), table.Budget.Slices)
			}
		}
	}

	for gpu := range int32(16) {
		if _, ok := seen[mig.CounterSetName(gpu)]; !ok {
			t.Errorf("no counter set for gpu %d", gpu)
		}
	}
}

// This is what makes overlapping profiles on one GPU mutually exclusive. If a
// device consumed nothing, every profile could be allocated simultaneously and
// the simulation would be worthless.
func TestBuildMIGSlicesDevicesConsumeTheirGPUsCounters(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(2, 0, false), testModel(), table, nodeA, NodeState{})
	_, devices := partition(slices)

	found := 0
	for _, s := range devices {
		for _, d := range s.Spec.Devices {
			if len(d.ConsumesCounters) != 1 {
				t.Fatalf("device %s consumes %d counter sets, want exactly 1",
					d.Name, len(d.ConsumesCounters))
			}
			consumption := d.ConsumesCounters[0]

			profileName := d.Attributes[AttrMIGProfile]
			if profileName.StringValue == nil {
				t.Fatalf("device %s has no migProfile attribute", d.Name)
			}

			var want mig.Profile
			for _, p := range table.Profiles {
				if p.Name == *profileName.StringValue {
					want = p
				}
			}
			if want.Name == "" {
				t.Fatalf("device %s has unknown profile %q", d.Name, *profileName.StringValue)
			}

			if got := consumption.Counters["slices"]; got.Value.Value() != int64(want.Slices) {
				t.Errorf("device %s consumes %d slices, want %d",
					d.Name, got.Value.Value(), want.Slices)
			}
			if got := consumption.Counters["memory"]; got.Value.Cmp(want.Memory) != 0 {
				t.Errorf("device %s consumes %v memory, want %v",
					d.Name, got.Value.String(), want.Memory.String())
			}
			found++
		}
	}

	if found != 2*len(table.Profiles) {
		t.Errorf("checked %d devices, want %d", found, 2*len(table.Profiles))
	}
}

// A GPU's profiles may straddle a device-slice boundary. The spike proved that
// is safe because counter sets resolve pool-wide, but every instance must still
// reference its own GPU's counter set across that boundary.
func TestBuildMIGSlicesCounterReferencesSurviveShardBoundaries(t *testing.T) {
	table := h100Table(t)
	// 11 GPUs x 6 profiles = 66 devices, so the second slice starts mid-GPU.
	slices := BuildMIGSlices(testPool(11, 4, true), testModel(), table, nodeA, NodeState{})
	_, devices := partition(slices)

	if len(devices) < 2 {
		t.Fatalf("expected the devices to shard, got %d slice(s)", len(devices))
	}

	declared := map[string]struct{}{}
	counters, _ := partition(slices)
	for _, s := range counters {
		for _, cs := range s.Spec.SharedCounters {
			declared[cs.Name] = struct{}{}
		}
	}

	for _, s := range devices {
		for _, d := range s.Spec.Devices {
			set := d.ConsumesCounters[0].CounterSet
			if _, ok := declared[set]; !ok {
				t.Errorf("device %s in slice %s references undeclared counter set %q",
					d.Name, s.Name, set)
			}
		}
	}
}

func TestBuildMIGSlicesDeviceAttributes(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(8, 4, true), testModel(), table, nodeA, NodeState{})
	_, devices := partition(slices)

	d := devices[0].Spec.Devices[0]

	stringAttrs := map[resourcev1.QualifiedName]string{
		AttrProductName: productH100,
		AttrVendor:      vendorNVIDIA,
		AttrMIGProfile:  table.Profiles[0].Name,
		AttrUUID:        DeviceUUID(nodeA, 0),
	}
	for name, want := range stringAttrs {
		attr, ok := d.Attributes[name]
		if !ok || attr.StringValue == nil {
			t.Errorf("attribute %q missing", name)
			continue
		}
		if *attr.StringValue != want {
			t.Errorf("attribute %q = %q, want %q", name, *attr.StringValue, want)
		}
	}

	// Topology comes from the physical GPU, so instances inherit it and a
	// topology-aware selector still works under MIG.
	if _, ok := d.Attributes[AttrNVLinkDomain]; !ok {
		t.Error("nvlinkDomain missing; MIG instances should inherit their GPU's topology")
	}
	if _, ok := d.Attributes[AttrNUMANode]; !ok {
		t.Error("numaNode missing; MIG instances should inherit their GPU's topology")
	}

	if attr, ok := d.Attributes[AttrMIGInstanceID]; !ok || attr.IntValue == nil {
		t.Error("migInstanceID missing or not an int attribute")
	}

	// The instance's own memory, not the whole GPU's.
	memory, ok := d.Capacity["memory"]
	if !ok {
		t.Fatal("memory capacity missing")
	}
	if memory.Value.Cmp(table.Profiles[0].Memory) != 0 {
		t.Errorf("capacity memory = %v, want the profile's %v",
			memory.Value.String(), table.Profiles[0].Memory.String())
	}
}

// A restarted operator must republish byte-identical slices rather than churn
// them, which at fleet scale is a significant write load.
func TestBuildMIGSlicesAreDeterministic(t *testing.T) {
	table := h100Table(t)

	first := BuildMIGSlices(testPool(16, 4, true), testModel(), table, nodeA, NodeState{})
	second := BuildMIGSlices(testPool(16, 4, true), testModel(), table, nodeA, NodeState{})

	if len(first) != len(second) {
		t.Fatalf("slice count differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("slice %d name differs: %q vs %q", i, first[i].Name, second[i].Name)
		}
		if len(first[i].Spec.Devices) != len(second[i].Spec.Devices) {
			t.Errorf("slice %s device count differs", first[i].Name)
		}
		for j := range first[i].Spec.Devices {
			if first[i].Spec.Devices[j].Name != second[i].Spec.Devices[j].Name {
				t.Errorf("slice %s device %d differs: %q vs %q", first[i].Name, j,
					first[i].Spec.Devices[j].Name, second[i].Spec.Devices[j].Name)
			}
		}
	}
}

func TestBuildMIGSlicesCarryPoolLabelViaName(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(8, 4, true), testModel(), table, nodeA, NodeState{})

	for _, s := range slices {
		if s.Name == "" {
			t.Error("slice built with an empty name")
		}
	}
}

// GPUPool validation requires at least one GPU, but the builder is a pure
// function reachable from anywhere and must not emit an empty slice that the
// API would reject.
func TestBuildMIGSlicesZeroGPUs(t *testing.T) {
	slices := BuildMIGSlices(testPool(0, 0, false), testModel(), h100Table(t), nodeA, NodeState{})
	if len(slices) != 0 {
		t.Errorf("got %d slices for 0 GPUs, want none", len(slices))
	}
}

// Under a declared partition the slices carry exactly the instances the
// administrator created, with an ordinal distinguishing repeats of one profile.
func TestBuildMIGSlicesWithDeclaredPartition(t *testing.T) {
	table := h100Table(t)
	pool := testPool(4, 4, true)
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	pool.Spec.MIGPartition = []v1alpha1.MIGPartitionEntry{
		{Profile: profileThird, Count: 1},
		{Profile: profileSmallest, Count: 4},
	}

	slices := BuildMIGSlices(pool, testModel(), table, nodeA, NodeState{})
	counters, devices := partition(slices)

	if len(counters) != 1 {
		t.Errorf("got %d counter slices, want 1 for 4 GPUs", len(counters))
	}

	names := map[string]struct{}{}
	total := 0
	for _, s := range devices {
		for _, d := range s.Spec.Devices {
			names[d.Name] = struct{}{}
			total++
		}
	}

	// 4 GPUs x 5 declared instances.
	if total != 20 {
		t.Errorf("got %d devices, want 20 (4 GPUs x 5 declared instances)", total)
	}
	for _, want := range []string{"gpu-0-3g-40gb-0", "gpu-0-1g-10gb-3", "gpu-3-1g-10gb-0"} {
		if _, ok := names[want]; !ok {
			t.Errorf("declared instance %q missing", want)
		}
	}
	for name := range names {
		if strings.Contains(name, profileWholeGPU) {
			t.Errorf("published %q, which the partition does not create", name)
		}
	}
}

// A profile whose memory equals the whole budget must still be expressible;
// this is the 7g.80gb case, which is the entire GPU.
func TestBuildMIGSlicesWholeGPUProfile(t *testing.T) {
	table := h100Table(t)
	slices := BuildMIGSlices(testPool(1, 0, false), testModel(), table, nodeA, NodeState{})
	_, devices := partition(slices)

	var whole *resourcev1.Device
	for i, d := range devices[0].Spec.Devices {
		if attr := d.Attributes[AttrMIGProfile]; attr.StringValue != nil && *attr.StringValue == profileWholeGPU {
			whole = &devices[0].Spec.Devices[i]
		}
	}
	if whole == nil {
		t.Fatal("no 7g.80gb device published")
	}

	consumption := whole.ConsumesCounters[0]
	if got := consumption.Counters["memory"]; got.Value.Cmp(resource.MustParse("80Gi")) != 0 {
		t.Errorf("whole-GPU profile consumes %v memory, want the full 80Gi", got.Value.String())
	}
	if got := consumption.Counters["slices"]; got.Value.Value() != 7 {
		t.Errorf("whole-GPU profile consumes %d slices, want all 7", got.Value.Value())
	}
}
