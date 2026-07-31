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

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// xidFellOffBus is the XID a GPU reports when it stops responding — the
// canonical "this card is gone" failure.
const xidFellOffBus = 79

func faultPool(entries ...v1alpha1.FaultEntry) *v1alpha1.GPUPool {
	pool := occupancyPool()
	pool.Spec.Faults = entries
	return pool
}

// Evict is the effect that makes fault injection worth having: a spike verified
// NoExecute throws a running workload off the device and releases its claim.
func TestFaultedTaintEffects(t *testing.T) {
	evict := FaultedTaint(v1alpha1.FaultEffectEvict, xidFellOffBus)
	if evict.Effect != resourcev1.DeviceTaintEffectNoExecute {
		t.Errorf("Evict effect = %q, want NoExecute", evict.Effect)
	}
	if evict.Key != "ghostgpu.dev/faulted" {
		t.Errorf("taint key = %q, want ghostgpu.dev/faulted", evict.Key)
	}
	// The XID rides on the taint so `kubectl get resourceslice` explains why a
	// device is out of service without anyone scraping metrics.
	if evict.Value != "xid-79" {
		t.Errorf("taint value = %q, want xid-79", evict.Value)
	}

	drain := FaultedTaint(v1alpha1.FaultEffectUnschedulable, 0)
	if drain.Effect != resourcev1.DeviceTaintEffectNoSchedule {
		t.Errorf("Unschedulable effect = %q, want NoSchedule", drain.Effect)
	}
	if drain.Value != "unspecified" {
		t.Errorf("taint value = %q, want unspecified when no XID is declared", drain.Value)
	}
}

// The occupied and faulted taints must stay distinct: a consumer tolerating
// "busy" should not thereby land on a dead card.
func TestFaultedAndOccupiedTaintsDiffer(t *testing.T) {
	if FaultedTaint(v1alpha1.FaultEffectEvict, 0).Key == OccupiedTaint().Key {
		t.Error("faulted and occupied share a taint key, so a toleration for one covers the other")
	}
}

func TestStateForResolvesFaults(t *testing.T) {
	pool := faultPool(
		v1alpha1.FaultEntry{
			NodeSelector: map[string]string{rackLabel: "a"},
			GPUs:         2,
			Effect:       v1alpha1.FaultEffectEvict,
			XID:          xidFellOffBus,
		},
	)

	state := StateFor(pool, occupancyNode(map[string]string{rackLabel: "a"}))
	if state.Faulted != 2 || state.XID != xidFellOffBus {
		t.Errorf("state = %+v, want 2 faulted with XID 79", state)
	}

	healthy := StateFor(pool, occupancyNode(map[string]string{rackLabel: "b"}))
	if healthy.Faulted != 0 {
		t.Errorf("unmatched node has %d faulted, want 0", healthy.Faulted)
	}
}

// Effect defaults to Evict, which is the failure people actually want to test:
// a card that merely stops accepting work does not exercise remediation.
func TestStateForDefaultsEffectToEvict(t *testing.T) {
	state := StateFor(faultPool(v1alpha1.FaultEntry{GPUs: 1}), occupancyNode(nil))
	if state.Effect != v1alpha1.FaultEffectEvict {
		t.Errorf("effect = %q, want Evict", state.Effect)
	}
}

// Faults and occupancy are independent statements about the same cards. "Three
// busy and one failed" means the failure happened to a GPU that was working —
// not that four are gone.
func TestFaultsAndOccupancyOverlap(t *testing.T) {
	pool := faultPool(v1alpha1.FaultEntry{GPUs: 1, XID: xidFellOffBus})
	pool.Spec.Occupancy = []v1alpha1.OccupancyEntry{{BusyPerNode: 3}}

	state := StateFor(pool, occupancyNode(nil))

	if state.Unavailable() != 3 {
		t.Errorf("unavailable = %d, want 3 — the fault hit a card that was already busy",
			state.Unavailable())
	}
	if !state.IsFaulted(0) {
		t.Error("gpu-0 should be faulted")
	}
	// A faulted GPU is never also reported occupied: it is not merely busy.
	if state.IsOccupied(0) {
		t.Error("gpu-0 is reported both faulted and occupied")
	}
	if !state.IsOccupied(1) || !state.IsOccupied(2) {
		t.Error("gpu-1 and gpu-2 should be occupied but healthy")
	}
	if state.IsOccupied(3) || state.IsFaulted(3) {
		t.Error("gpu-3 should be free")
	}
}

func TestBuildResourceSliceTaintsFaultedDevices(t *testing.T) {
	pool := faultPool()
	pool.Spec.GPUsPerNode = 3
	model := &v1alpha1.GPUModel{Spec: v1alpha1.GPUModelSpec{ProductName: "H100"}}

	slice := BuildResourceSlice(pool, model, "node-a", NodeState{
		Faulted: 1, Busy: 2, Effect: v1alpha1.FaultEffectEvict, XID: xidFellOffBus,
	})

	if len(slice.Spec.Devices) != 3 {
		t.Fatalf("published %d devices, want 3 — a failed GPU is still published", len(slice.Spec.Devices))
	}
	if got := slice.Spec.Devices[0].Taints[0]; got.Key != FaultedTaintKey ||
		got.Effect != resourcev1.DeviceTaintEffectNoExecute {
		t.Errorf("gpu-0 taint = %+v, want a NoExecute fault", got)
	}
	if got := slice.Spec.Devices[1].Taints[0]; got.Key != OccupiedTaintKey {
		t.Errorf("gpu-1 taint = %+v, want the occupied taint", got)
	}
	if len(slice.Spec.Devices[2].Taints) != 0 {
		t.Errorf("gpu-2 is tainted %+v, want healthy and free", slice.Spec.Devices[2].Taints)
	}
}

// A failed card is not available either, so it comes out of allocatable
// alongside occupied ones — otherwise a workload could be placed on hardware
// the simulation says is broken.
func TestNodeAllocatableSubtractsFaults(t *testing.T) {
	pool := faultPool()
	pool.Spec.GPUsPerNode = 8

	allocatable := NodeAllocatable(pool, mig.Table{}, NodeState{Faulted: 3})
	if got := allocatable[GPUResourceName]; got.Value() != 5 {
		t.Errorf("allocatable = %d, want 5 with three cards failed", got.Value())
	}

	capacity := NodeResources(pool, mig.Table{})
	if got := capacity[GPUResourceName]; got.Value() != 8 {
		t.Errorf("capacity = %d, want the hardware's full 8", got.Value())
	}
}

func TestValidateFaults(t *testing.T) {
	if err := ValidateFaults(faultPool(v1alpha1.FaultEntry{GPUs: 9})); err == nil {
		t.Error("want an error when more GPUs fail than the node has")
	}
	if err := ValidateFaults(faultPool(v1alpha1.FaultEntry{GPUs: -1})); err == nil {
		t.Error("want an error for a negative count")
	}
	if err := ValidateFaults(faultPool(v1alpha1.FaultEntry{GPUs: 1, Effect: "Explode"})); err == nil {
		t.Error("want an error for an unknown effect")
	}
	if err := ValidateFaults(faultPool(v1alpha1.FaultEntry{GPUs: 8})); err != nil {
		t.Errorf("every GPU failing is a legal, if bleak, fleet: %v", err)
	}
}
