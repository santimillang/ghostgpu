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

package cli

import (
	"strings"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
)

const testNode = "node-a"

func statusPool(name string, mode v1alpha1.SharingMode, nodes, devices int32) v1alpha1.GPUPool {
	return v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.GPUPoolSpec{SharingMode: mode, GPUsPerNode: 8},
		Status: v1alpha1.GPUPoolStatus{
			NodesMatched:     nodes,
			DevicesPublished: devices,
		},
	}
}

// deviceSlice builds a published slice. Device names follow the operator's
// scheme so the GPU index can be recovered from them.
func deviceSlice(poolName, node string, devices ...resourcev1.Device) resourcev1.ResourceSlice {
	return resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:   poolName + "-" + node + "-devices-0",
			Labels: map[string]string{"ghostgpu.dev/pool": poolName},
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:  gpu.DriverName,
			Pool:    resourcev1.ResourcePool{Name: node},
			Devices: devices,
		},
	}
}

func wholeDevice(name string) resourcev1.Device {
	return resourcev1.Device{Name: name}
}

func occupiedDevice(name string) resourcev1.Device {
	return resourcev1.Device{
		Name:   name,
		Taints: []resourcev1.DeviceTaint{gpu.OccupiedTaint()},
	}
}

// Occupied and allocated are different states with different owners: one is the
// pool spec's doing, the other the scheduler's. Reporting occupancy as an
// allocation would invent a holder that does not exist, and reporting it as
// free would describe a fleet with more room than the scheduler can see.
func TestBuildReportSeparatesOccupiedFromAllocated(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 1, 4)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", testNode,
				occupiedDevice("gpu-0"),
				occupiedDevice("gpu-1"),
				wholeDevice("gpu-2"),
				wholeDevice("gpu-3")),
		},
		[]resourcev1.ResourceClaim{claim("default", testNode, "gpu-2", "trainer")},
	)

	if len(report.Pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(report.Pools))
	}
	p := report.Pools[0]
	if p.Occupied != 2 {
		t.Errorf("occupied = %d, want 2", p.Occupied)
	}
	if p.Allocated != 1 {
		t.Errorf("allocated = %d, want 1", p.Allocated)
	}

	byName := map[string]DeviceStatus{}
	for _, d := range report.Devices {
		byName[d.Device] = d
	}
	if !byName["gpu-0"].Occupied || byName["gpu-0"].Allocated {
		t.Errorf("gpu-0 = %+v, want occupied and not allocated", byName["gpu-0"])
	}
	if byName["gpu-2"].Occupied || !byName["gpu-2"].Allocated {
		t.Errorf("gpu-2 = %+v, want allocated and not occupied", byName["gpu-2"])
	}
	if byName["gpu-3"].Occupied || byName["gpu-3"].Allocated {
		t.Errorf("gpu-3 = %+v, want free", byName["gpu-3"])
	}
}

func TestRenderPoolsSubtractsOccupiedFromFree(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 1, 4)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", testNode,
				occupiedDevice("gpu-0"),
				occupiedDevice("gpu-1"),
				wholeDevice("gpu-2"),
				wholeDevice("gpu-3")),
		},
		[]resourcev1.ResourceClaim{claim("default", testNode, "gpu-2", "trainer")},
	)

	out := RenderPools(report)
	if !strings.Contains(out, "OCCUPIED") {
		t.Errorf("output has no OCCUPIED column:\n%s", out)
	}

	// Assert on fields rather than spacing: the column widths are the
	// tabwriter's business and would make this brittle for no gain.
	// 4 devices, 2 occupied, 1 allocated, so exactly 1 is genuinely free.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and one row, got:\n%s", out)
	}
	fields := strings.Fields(lines[1])
	if got := strings.Join(fields[len(fields)-4:], " "); got != "4 2 1 1" {
		t.Errorf("devices/occupied/allocated/free = %q, want \"4 2 1 1\":\n%s", got, out)
	}
}

// A device declared busy has no pod behind it, so the holder column must not
// imply one.
func TestRenderDevicesLabelsOccupied(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 1, 2)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", testNode, occupiedDevice("gpu-0"), wholeDevice("gpu-1")),
		},
		nil,
	)

	out := RenderDevices(report, "")
	if !strings.Contains(out, "occupied") {
		t.Errorf("occupied device not labelled:\n%s", out)
	}
	if !strings.Contains(out, "(declared)") {
		t.Errorf("occupied device should say who declared it, not name a pod:\n%s", out)
	}
}

func migDevice(name, profile string, slices int64, memory string) resourcev1.Device {
	value := profile
	return resourcev1.Device{
		Name: name,
		Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			gpu.AttrMIGProfile: {StringValue: &value},
		},
		ConsumesCounters: []resourcev1.DeviceCounterConsumption{{
			CounterSet: strings.Join(strings.Split(name, "-")[:2], "-"),
			Counters: map[string]resourcev1.Counter{
				"slices": {Value: *resource.NewQuantity(slices, resource.DecimalSI)},
				"memory": {Value: resource.MustParse(memory)},
			},
		}},
	}
}

func counterSlice(poolName, node string, sets ...resourcev1.CounterSet) resourcev1.ResourceSlice {
	return resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:   poolName + "-" + node + "-counters-0",
			Labels: map[string]string{"ghostgpu.dev/pool": poolName},
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:         gpu.DriverName,
			Pool:           resourcev1.ResourcePool{Name: node},
			SharedCounters: sets,
		},
	}
}

func gpuCounterSet(name string, slices int64, memory string) resourcev1.CounterSet {
	return resourcev1.CounterSet{
		Name: name,
		Counters: map[string]resourcev1.Counter{
			"slices": {Value: *resource.NewQuantity(slices, resource.DecimalSI)},
			"memory": {Value: resource.MustParse(memory)},
		},
	}
}

// claim builds an allocated ResourceClaim held by a pod. Its own name is
// irrelevant to every assertion here — what matters is the device it points at
// and the consumer holding it.
func claim(namespace, node, device, pod string) resourcev1.ResourceClaim {
	return resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pod + "-claim", Namespace: namespace},
		Status: resourcev1.ResourceClaimStatus{
			Allocation: &resourcev1.AllocationResult{
				Devices: resourcev1.DeviceAllocationResult{
					Results: []resourcev1.DeviceRequestAllocationResult{{
						Driver: gpu.DriverName,
						Pool:   node,
						Device: device,
					}},
				},
			},
			ReservedFor: []resourcev1.ResourceClaimConsumerReference{{
				Resource: "pods",
				Name:     pod,
			}},
		},
	}
}

func TestBuildReportSummarisesPools(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeMIG, 2, 96)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", testNode, wholeDevice("gpu-0"), wholeDevice("gpu-1")),
		},
		[]resourcev1.ResourceClaim{claim("default", testNode, "gpu-0", "trainer")},
	)

	if len(report.Pools) != 1 {
		t.Fatalf("got %d pools, want 1", len(report.Pools))
	}
	p := report.Pools[0]
	if p.Name != "h100-pool" {
		t.Errorf("name = %q", p.Name)
	}
	if p.Mode != string(v1alpha1.SharingModeMIG) {
		t.Errorf("mode = %q, want mig", p.Mode)
	}
	if p.Nodes != 2 || p.Devices != 96 {
		t.Errorf("nodes/devices = %d/%d, want 2/96", p.Nodes, p.Devices)
	}
	if p.Allocated != 1 {
		t.Errorf("allocated = %d, want 1", p.Allocated)
	}
}

// Attribution is what users currently work out by hand from ResourceClaim
// jsonpath. Getting it from the claim's consumer is the whole point.
func TestBuildReportAttributesDevicesToPods(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 1, 2)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", testNode, wholeDevice("gpu-0"), wholeDevice("gpu-1")),
		},
		[]resourcev1.ResourceClaim{claim("team-a", testNode, "gpu-1", "trainer-b")},
	)

	byName := map[string]DeviceStatus{}
	for _, d := range report.Devices {
		byName[d.Device] = d
	}

	held := byName["gpu-1"]
	if held.Pod != "trainer-b" || held.Namespace != "team-a" {
		t.Errorf("gpu-1 held by %s/%s, want team-a/trainer-b", held.Namespace, held.Pod)
	}
	if free := byName["gpu-0"]; free.Pod != "" {
		t.Errorf("gpu-0 reported as held by %q, want free", free.Pod)
	}
}

// A claim for another driver's devices must not be attributed to ghostgpu's,
// or a cluster running a real GPU driver alongside would show phantom holders.
func TestBuildReportIgnoresOtherDrivers(t *testing.T) {
	foreign := claim("default", testNode, "gpu-0", "trainer")
	foreign.Status.Allocation.Devices.Results[0].Driver = "gpu.example.com"

	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 1, 1)},
		[]resourcev1.ResourceSlice{deviceSlice("h100-pool", testNode, wholeDevice("gpu-0"))},
		[]resourcev1.ResourceClaim{foreign},
	)

	if report.Pools[0].Allocated != 0 {
		t.Errorf("allocated = %d, want 0; another driver's claim was counted",
			report.Pools[0].Allocated)
	}
	if report.Devices[0].Pod != "" {
		t.Errorf("gpu-0 attributed to %q from another driver's claim", report.Devices[0].Pod)
	}
}

// An allocated claim nothing has picked up yet is genuinely different from a
// free device, and saying so avoids a confusing blank column.
func TestBuildReportHandlesUnreservedClaims(t *testing.T) {
	unreserved := claim("default", testNode, "gpu-0", "")
	unreserved.Status.ReservedFor = nil

	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 1, 1)},
		[]resourcev1.ResourceSlice{deviceSlice("h100-pool", testNode, wholeDevice("gpu-0"))},
		[]resourcev1.ResourceClaim{unreserved},
	)

	if report.Pools[0].Allocated != 1 {
		t.Errorf("allocated = %d, want 1; the device is held even with no consumer",
			report.Pools[0].Allocated)
	}
	if got := report.Devices[0].Pod; got != "" {
		t.Errorf("pod = %q, want empty for an unreserved claim", got)
	}
	if !report.Devices[0].Allocated {
		t.Error("device should be marked allocated even without a consumer")
	}
}

func TestBuildReportCarriesMIGProfile(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeMIG, 1, 2)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", testNode,
				migDevice("gpu-0-1g-10gb", "1g.10gb", 1, "10Gi"),
				migDevice("gpu-0-7g-80gb", "7g.80gb", 7, h100Memory)),
		},
		nil,
	)

	for _, d := range report.Devices {
		if d.Profile == "" {
			t.Errorf("device %s has no profile", d.Device)
		}
	}
}

// Working out why a MIG instance cannot be allocated means summing the
// consumption of everything already holding that GPU's budget, across several
// slices. That is the single most tedious thing to do by hand.
func TestBuildReportComputesGPUBudgets(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeMIG, 1, 2)},
		[]resourcev1.ResourceSlice{
			counterSlice("h100-pool", testNode, gpuCounterSet("gpu-0", 7, h100Memory)),
			deviceSlice("h100-pool", testNode,
				migDevice("gpu-0-3g-40gb", "3g.40gb", 3, "40Gi"),
				migDevice("gpu-0-1g-10gb", "1g.10gb", 1, "10Gi")),
		},
		[]resourcev1.ResourceClaim{
			claim("default", testNode, "gpu-0-3g-40gb", "trainer"),
		},
	)

	if len(report.GPUs) != 1 {
		t.Fatalf("got %d GPU budgets, want 1", len(report.GPUs))
	}
	g := report.GPUs[0]
	if g.CounterSet != "gpu-0" {
		t.Errorf("counter set = %q", g.CounterSet)
	}
	if g.TotalSlices != 7 {
		t.Errorf("total slices = %d, want 7", g.TotalSlices)
	}
	if g.UsedSlices != 3 {
		t.Errorf("used slices = %d, want 3 (only the allocated 3g.40gb)", g.UsedSlices)
	}
	if g.UsedMemory.Cmp(resource.MustParse("40Gi")) != 0 {
		t.Errorf("used memory = %v, want 40Gi", g.UsedMemory.String())
	}
	if g.TotalMemory.Cmp(resource.MustParse(h100Memory)) != 0 {
		t.Errorf("total memory = %v, want 80Gi", g.TotalMemory.String())
	}
}

func TestRenderPoolsIsTabular(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{
			statusPool("h100-pool", v1alpha1.SharingModeMIG, 2, 96),
			statusPool("a100-pool", v1alpha1.SharingModeNone, 1, 8),
		},
		nil, nil,
	)

	out := RenderPools(report)
	wantContent := []string{
		"POOL", "MODE", "NODES", "DEVICES",
		"h100-pool", "a100-pool", string(v1alpha1.SharingModeMIG),
	}
	for _, want := range wantContent {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDevicesFiltersByNode(t *testing.T) {
	report := BuildReport(
		[]v1alpha1.GPUPool{statusPool("h100-pool", v1alpha1.SharingModeNone, 2, 2)},
		[]resourcev1.ResourceSlice{
			deviceSlice("h100-pool", "node-a", wholeDevice("gpu-0")),
			deviceSlice("h100-pool", "node-b", wholeDevice("gpu-0")),
		},
		[]resourcev1.ResourceClaim{claim("default", "node-b", "gpu-0", "trainer-b")},
	)

	out := RenderDevices(report, "node-b")
	if !strings.Contains(out, "trainer-b") {
		t.Errorf("node-b view missing its pod:\n%s", out)
	}
	if strings.Count(out, "gpu-0") != 1 {
		t.Errorf("expected exactly one device row for node-b:\n%s", out)
	}
}

// Sorting keeps repeated runs comparable, which matters when the output is
// pasted into an issue or diffed between two states.
func TestReportOrderingIsStable(t *testing.T) {
	pools := []v1alpha1.GPUPool{
		statusPool("zebra", v1alpha1.SharingModeNone, 1, 1),
		statusPool("alpha", v1alpha1.SharingModeNone, 1, 1),
	}
	slices := []resourcev1.ResourceSlice{
		deviceSlice("zebra", "node-b", wholeDevice("gpu-1"), wholeDevice("gpu-0")),
		deviceSlice("alpha", "node-a", wholeDevice("gpu-0")),
	}

	first := BuildReport(pools, slices, nil)
	second := BuildReport(pools, slices, nil)

	if first.Pools[0].Name != "alpha" {
		t.Errorf("pools not sorted: first is %q", first.Pools[0].Name)
	}
	for i := range first.Devices {
		if first.Devices[i].Device != second.Devices[i].Device {
			t.Errorf("device order differs between runs at %d", i)
		}
	}
	if first.Devices[0].Node != "node-a" {
		t.Errorf("devices not sorted by node: first is %q", first.Devices[0].Node)
	}
}
