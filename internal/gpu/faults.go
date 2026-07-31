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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// FaultedTaintKey marks a published device as failed hardware.
//
// Distinct from the occupied taint because the two mean different things to
// anything reading them: an occupied GPU is working and busy, a faulted one is
// broken. A consumer tolerating "busy" should not thereby land on a dead card.
const FaultedTaintKey = "ghostgpu.dev/faulted"

// FaultedTaint is the device taint marking a simulated GPU as failed.
//
// Evict becomes NoExecute, which a spike verified does throw a running workload
// off the device and release its ResourceClaim — with a negative control
// confirming an identical untainted pod survives. That deallocation is the
// point: it is what lets the job reschedule onto healthy hardware, which is the
// behaviour a remediation system under test has to exercise.
func FaultedTaint(effect v1alpha1.FaultEffect, xid int32) resourcev1.DeviceTaint {
	taint := resourcev1.DeviceTaint{
		Key:    FaultedTaintKey,
		Value:  faultValue(xid),
		Effect: resourcev1.DeviceTaintEffectNoSchedule,
	}
	if effect != v1alpha1.FaultEffectUnschedulable {
		taint.Effect = resourcev1.DeviceTaintEffectNoExecute
	}
	return taint
}

// faultValue records the XID in the taint, so `kubectl get resourceslice` shows
// why a device is out of service without anyone having to scrape metrics.
func faultValue(xid int32) string {
	if xid <= 0 {
		return "unspecified"
	}
	return fmt.Sprintf("xid-%d", xid)
}

// NodeState is everything a pool declares about one node's GPUs.
//
// Grouped into a struct rather than passed as a growing list of counts: the
// slice builders need all of it, and each new failure mode would otherwise add
// another positional parameter to every call site.
type NodeState struct {
	// Busy is how many GPUs are declared occupied.
	Busy int32

	// Faulted is how many have failed.
	Faulted int32

	// Effect and XID describe the failure, and are meaningless when Faulted
	// is zero.
	Effect v1alpha1.FaultEffect
	XID    int32
}

// Unavailable is how many of the node's GPUs cannot take new work.
//
// The maximum rather than the sum, because faults and occupancy are independent
// statements about the same cards: "three are busy and one has failed" means
// the failure happened to a GPU that was working, not that four are gone.
func (s NodeState) Unavailable() int32 {
	return max(s.Busy, s.Faulted)
}

// IsFaulted reports whether a GPU index has failed.
func (s NodeState) IsFaulted(index int32) bool { return index < s.Faulted }

// IsOccupied reports whether a GPU index is declared busy but working.
//
// A faulted GPU is never also reported occupied. It is not merely busy, and
// reporting it as such would understate what happened to the fleet.
func (s NodeState) IsOccupied(index int32) bool {
	return !s.IsFaulted(index) && index < s.Busy
}

// Taint returns the device taint for a GPU index, or nil when it is healthy and
// free.
func (s NodeState) Taint(index int32) *resourcev1.DeviceTaint {
	switch {
	case s.IsFaulted(index):
		taint := FaultedTaint(s.Effect, s.XID)
		return &taint
	case s.IsOccupied(index):
		taint := OccupiedTaint()
		return &taint
	default:
		return nil
	}
}

// StateFor resolves what a pool declares about one node.
func StateFor(pool *v1alpha1.GPUPool, node *corev1.Node) NodeState {
	state := NodeState{Busy: BusyGPUs(pool, node)}

	for _, fault := range pool.Spec.Faults {
		if !selectorMatches(fault.NodeSelector, node.Labels) {
			continue
		}
		state.Faulted = max(0, min(fault.GPUs, pool.Spec.GPUsPerNode))
		state.Effect = fault.Effect
		if state.Effect == "" {
			state.Effect = v1alpha1.FaultEffectEvict
		}
		state.XID = fault.XID
		break
	}

	return state
}

// ValidateFaults rejects a declaration the pool's hardware cannot carry.
//
// Reported on status rather than clamped, for the same reason occupancy is:
// "six of my four GPUs have failed" describes a fleet that does not exist, and
// simulating the nearest one that does would hide the mistake inside the very
// scenario being reasoned about.
func ValidateFaults(pool *v1alpha1.GPUPool) error {
	for _, fault := range pool.Spec.Faults {
		if fault.GPUs < 0 {
			return fmt.Errorf("faults %v: gpus is %d; a negative count of failed GPUs is not a fleet",
				fault.NodeSelector, fault.GPUs)
		}
		if fault.GPUs > pool.Spec.GPUsPerNode {
			return fmt.Errorf("faults %v: gpus is %d but the pool gives each node %d GPUs",
				fault.NodeSelector, fault.GPUs, pool.Spec.GPUsPerNode)
		}
		switch fault.Effect {
		case "", v1alpha1.FaultEffectEvict, v1alpha1.FaultEffectUnschedulable:
		default:
			return fmt.Errorf("faults %v: unknown effect %q; want Evict or Unschedulable",
				fault.NodeSelector, fault.Effect)
		}
	}
	return nil
}
