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

	"k8s.io/utils/ptr"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// h100MiB is 80Gi expressed the way DCGM reports framebuffer: whole MiB.
const h100MiB = 81920

// TestMetricNamesMatchDCGM pins every name to the literal string dcgm-exporter
// uses.
//
// These are an external contract in the strongest sense the project has:
// dashboards, recording rules and KEDA queries hardcode them, so a rename makes
// ghostgpu invisible to the tooling it exists to test. Taken from
// dcgm-exporter's default counter set, not from memory.
func TestMetricNamesMatchDCGM(t *testing.T) {
	for name, want := range map[string]string{
		GPUUtil:        "DCGM_FI_DEV_GPU_UTIL",
		MemCopyUtil:    "DCGM_FI_DEV_MEM_COPY_UTIL",
		FBUsed:         "DCGM_FI_DEV_FB_USED",
		FBFree:         "DCGM_FI_DEV_FB_FREE",
		GREngineActive: "DCGM_FI_PROF_GR_ENGINE_ACTIVE",
		TensorActive:   "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE",
		PowerUsage:     "DCGM_FI_DEV_POWER_USAGE",
		GPUTemp:        "DCGM_FI_DEV_GPU_TEMP",
		XIDErrors:      "DCGM_FI_DEV_XID_ERRORS",
	} {
		if name != want {
			t.Errorf("metric name = %q, want %q", name, want)
		}
	}

	for label, want := range map[string]string{
		LabelGPU:           "gpu",
		LabelUUID:          "UUID",
		LabelDevice:        "device",
		LabelModelName:     "modelName",
		LabelHostname:      "Hostname",
		LabelNamespace:     "namespace",
		LabelPod:           "pod",
		LabelContainer:     "container",
		LabelMIGInstanceID: "GPU_I_ID",
		LabelMIGProfile:    "GPU_I_PROFILE",
	} {
		if label != want {
			t.Errorf("label = %q, want %q", label, want)
		}
	}
}

func find(t *testing.T, series []Series, name string, match map[string]string) Series {
	t.Helper()

	for _, s := range series {
		if s.Name != name {
			continue
		}
		ok := true
		for k, v := range match {
			if s.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return s
		}
	}
	t.Fatalf("no %s series matching %v in %d series", name, match, len(series))
	return Series{}
}

func idleCard() Card {
	return Card{
		Node: "node-a", Index: 0, UUID: "GPU-abc", ProductName: "NVIDIA H100 80GB HBM3",
		MemoryMiB: h100MiB, Reading: Resolve(nil, false),
	}
}

func TestBuildIdleCardReportsZero(t *testing.T) {
	series := Build([]Card{idleCard()})

	if got := find(t, series, GPUUtil, nil).Value; got != 0 {
		t.Errorf("idle GPU util = %v, want 0", got)
	}
	if got := find(t, series, FBUsed, nil).Value; got != 0 {
		t.Errorf("idle framebuffer used = %v, want 0", got)
	}
	if got := find(t, series, FBFree, nil).Value; got != h100MiB {
		t.Errorf("idle framebuffer free = %v, want the whole %d MiB", got, h100MiB)
	}
}

// An idle GPU has no pod, and an empty pod label is a distinct time series that
// `sum by (pod)` would happily group on — which is exactly the confusion the
// real exporter's bug reports describe.
func TestBuildOmitsWorkloadLabelsWhenNothingHoldsTheDevice(t *testing.T) {
	series := Build([]Card{idleCard()})

	s := find(t, series, GPUUtil, nil)
	for _, label := range []string{LabelNamespace, LabelPod, LabelContainer} {
		if _, present := s.Labels[label]; present {
			t.Errorf("idle device carries a %q label: %v", label, s.Labels)
		}
	}
}

// Attribution is the differentiator: ghostgpu knows exactly which pod holds a
// device because the scheduler wrote it into ResourceClaim.status.
func TestBuildAttributesAllocatedDevicesToTheirPod(t *testing.T) {
	card := idleCard()
	card.Holder = Holder{Namespace: testNamespace, Pod: "trainer-0", Container: testWorkload}
	card.Reading = Resolve(nil, true)

	series := Build([]Card{card})

	s := find(t, series, GPUUtil, map[string]string{LabelPod: "trainer-0"})
	if s.Value != 100 {
		t.Errorf("allocated GPU util = %v, want the busy default of 100", s.Value)
	}
	if s.Labels[LabelNamespace] != testNamespace || s.Labels[LabelContainer] != testWorkload {
		t.Errorf("labels = %v, want namespace team-a and container trainer", s.Labels)
	}
	if s.Labels[LabelHostname] != "node-a" {
		t.Errorf("Hostname = %q, want the simulated node", s.Labels[LabelHostname])
	}
}

func TestBuildFramebufferFollowsDeclaredPercentage(t *testing.T) {
	card := idleCard()
	card.Reading = Resolve(&v1alpha1.UtilizationSpec{
		WhenAllocated: &v1alpha1.UtilizationSample{FBUsedPercent: ptr.To(int32(75))},
	}, true)

	series := Build([]Card{card})

	used := find(t, series, FBUsed, nil).Value
	free := find(t, series, FBFree, nil).Value

	if used != h100MiB*0.75 {
		t.Errorf("used = %v, want 75%% of %d", used, h100MiB)
	}
	// Derived rather than declared separately, so the two can never disagree
	// about how big the card is.
	if used+free != h100MiB {
		t.Errorf("used + free = %v, want exactly the card's %d MiB", used+free, h100MiB)
	}
}

// DCGM reports these as ratios between 0 and 1; the spec takes percentages
// because a percentage is far easier to write in a manifest.
func TestBuildConvertsProfilingPercentagesToRatios(t *testing.T) {
	card := idleCard()
	card.Reading = Resolve(&v1alpha1.UtilizationSpec{
		WhenAllocated: &v1alpha1.UtilizationSample{
			GREngineActivePercent: ptr.To(int32(90)),
			TensorActivePercent:   ptr.To(int32(45)),
		},
	}, true)

	series := Build([]Card{card})

	if got := find(t, series, GREngineActive, nil).Value; got != 0.90 {
		t.Errorf("graphics engine active = %v, want the ratio 0.9", got)
	}
	if got := find(t, series, TensorActive, nil).Value; got != 0.45 {
		t.Errorf("tensor active = %v, want the ratio 0.45", got)
	}
}

// ghostgpu has no thermal or power model. A plausible-looking wattage would be
// fabrication rather than simulation, so the series is absent until declared.
func TestBuildOmitsPowerAndTemperatureUntilDeclared(t *testing.T) {
	series := Build([]Card{idleCard()})

	for _, s := range series {
		if s.Name == PowerUsage || s.Name == GPUTemp {
			t.Errorf("%s was emitted without being declared: %v", s.Name, s)
		}
	}

	card := idleCard()
	card.Reading = Resolve(&v1alpha1.UtilizationSpec{
		WhenIdle: &v1alpha1.UtilizationSample{
			PowerWatts:   ptr.To(int32(70)),
			TemperatureC: ptr.To(int32(31)),
		},
	}, false)

	series = Build([]Card{card})
	if got := find(t, series, PowerUsage, nil).Value; got != 70 {
		t.Errorf("power = %v, want the declared 70W", got)
	}
	if got := find(t, series, GPUTemp, nil).Value; got != 31 {
		t.Errorf("temperature = %v, want the declared 31C", got)
	}
}

// Under MIG, dcgm-exporter reports both the whole card and each instance.
// Physical readings stay at card level, because instances share one piece of
// silicon and a per-instance wattage is a number no hardware could produce.
func TestBuildReportsBothCardAndMIGInstances(t *testing.T) {
	card := idleCard()
	card.Reading = Resolve(&v1alpha1.UtilizationSpec{
		WhenIdle: &v1alpha1.UtilizationSample{PowerWatts: ptr.To(int32(300))},
	}, false)
	card.Instances = []Instance{
		{
			Profile: profileLarge, InstanceID: 3, MemoryMiB: 40960,
			Holder:  Holder{Namespace: defaultNamespace, Pod: testWorkload},
			Reading: Resolve(nil, true),
		},
		{Profile: "1g.10gb", InstanceID: 7, MemoryMiB: 10240, Reading: Resolve(nil, false)},
	}

	series := Build([]Card{card})

	busy := find(t, series, GPUUtil, map[string]string{LabelMIGProfile: profileLarge})
	if busy.Value != 100 || busy.Labels[LabelPod] != testWorkload {
		t.Errorf("allocated instance = %v labels %v, want 100 held by trainer", busy.Value, busy.Labels)
	}
	if busy.Labels[LabelMIGInstanceID] != "3" {
		t.Errorf("GPU_I_ID = %q, want 3", busy.Labels[LabelMIGInstanceID])
	}
	// The instance's own framebuffer, not the card's.
	if got := find(t, series, FBUsed, map[string]string{LabelMIGProfile: profileLarge}).Value; got != 40960 {
		t.Errorf("instance framebuffer used = %v, want its own 40960 MiB", got)
	}

	idle := find(t, series, GPUUtil, map[string]string{LabelMIGProfile: "1g.10gb"})
	if idle.Value != 0 {
		t.Errorf("idle instance util = %v, want 0", idle.Value)
	}
	if _, held := idle.Labels[LabelPod]; held {
		t.Errorf("idle instance carries a pod label: %v", idle.Labels)
	}

	// Power is a property of the card, so exactly one series and no GPU_I_*.
	var power []Series
	for _, s := range series {
		if s.Name == PowerUsage {
			power = append(power, s)
		}
	}
	if len(power) != 1 {
		t.Fatalf("got %d power series, want 1 — power is not a per-instance reading", len(power))
	}
	if _, perInstance := power[0].Labels[LabelMIGProfile]; perInstance {
		t.Errorf("power carries MIG labels: %v", power[0].Labels)
	}
}

// An explicit zero has to override a non-zero default, because zero is a
// meaningful reading rather than an absence.
func TestResolveDistinguishesExplicitZeroFromUnset(t *testing.T) {
	busy := Resolve(&v1alpha1.UtilizationSpec{
		WhenAllocated: &v1alpha1.UtilizationSample{GPUUtil: ptr.To(int32(0))},
	}, true)
	if busy.GPUUtil != 0 {
		t.Errorf("explicit 0 gave %d; a defaulted field swallowed it", busy.GPUUtil)
	}
	// Unset fields still take the busy default.
	if busy.FBUsedPercent != 100 {
		t.Errorf("unset framebuffer = %d, want the busy default 100", busy.FBUsedPercent)
	}
}

// Metrics output gets diffed between states in tests, and map iteration order
// would make that useless.
func TestBuildIsDeterministic(t *testing.T) {
	cards := []Card{idleCard(), {
		Node: "node-b", Index: 1, UUID: "GPU-def", ProductName: "NVIDIA H100 80GB HBM3",
		MemoryMiB: h100MiB, Reading: Resolve(nil, true),
	}}

	first, second := Build(cards), Build(cards)
	if len(first) != len(second) {
		t.Fatalf("different lengths: %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name || labelKey(first[i].Labels) != labelKey(second[i].Labels) {
			t.Fatalf("series %d differs between runs: %v vs %v", i, first[i], second[i])
		}
	}
}
