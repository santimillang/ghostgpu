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

// OccupiedTaintKey marks a published device as already busy.
//
// This is a DRA device taint carried inline on the ResourceSlice ghostgpu
// already owns, which is what makes declared occupancy honest: the upstream
// scheduler enforces it, and ghostgpu never writes ResourceClaim.status — the
// allocation state the scheduler owns. A spike confirmed the scheduler refuses
// a claim when only tainted devices remain, and allocates it the moment the
// taint is lifted, on a cluster with no extra feature gates.
//
// Consumers can write a toleration for this key to schedule onto occupied
// devices deliberately, so the key and effect are an external contract.
const OccupiedTaintKey = "ghostgpu.dev/occupied"

// OccupiedTaintValue records who occupied the device. A constant rather than a
// per-pool value: a toleration written against it has to keep matching when the
// pool is renamed.
const OccupiedTaintValue = "ghostgpu"

// OccupiedTaint is the device taint marking a simulated GPU as busy.
//
// NoSchedule rather than NoExecute: occupancy describes a fleet's starting
// state, and evicting already-placed workloads is a different feature that no
// scenario here asks for.
func OccupiedTaint() resourcev1.DeviceTaint {
	return resourcev1.DeviceTaint{
		Key:    OccupiedTaintKey,
		Value:  OccupiedTaintValue,
		Effect: resourcev1.DeviceTaintEffectNoSchedule,
	}
}

// BusyGPUs reports how many of a node's physical GPUs the pool declares busy.
//
// First match wins, so a trailing entry with no selector reads as a default and
// ordering in the manifest is meaningful. The result is clamped to the node's
// GPU count so that this stays total: an over-large declaration is a user error
// the controller reports on status, but it must not make the builders emit
// taints for devices that do not exist.
func BusyGPUs(pool *v1alpha1.GPUPool, node *corev1.Node) int32 {
	for _, entry := range pool.Spec.Occupancy {
		if !selectorMatches(entry.NodeSelector, node.Labels) {
			continue
		}
		return max(0, min(entry.BusyPerNode, pool.Spec.GPUsPerNode))
	}
	return 0
}

// selectorMatches reports whether every label in the selector is present on the
// node with the same value. An empty selector matches everything.
func selectorMatches(selector, labels map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

// ValidateOccupancy rejects a declaration the pool's hardware cannot carry.
//
// Reported on status rather than clamped silently. "Six of my four GPUs are
// busy" is a statement about a fleet that does not exist, and quietly
// simulating the nearest thing that does would hide the mistake in exactly the
// scenario the user is trying to reason about.
func ValidateOccupancy(pool *v1alpha1.GPUPool) error {
	for _, entry := range pool.Spec.Occupancy {
		if entry.BusyPerNode < 0 {
			return fmt.Errorf("occupancy %v: busyPerNode is %d; a negative count of busy GPUs is not a fleet",
				entry.NodeSelector, entry.BusyPerNode)
		}
		if entry.BusyPerNode > pool.Spec.GPUsPerNode {
			return fmt.Errorf(
				"occupancy %v: busyPerNode is %d but the pool gives each node %d GPUs",
				entry.NodeSelector, entry.BusyPerNode, pool.Spec.GPUsPerNode)
		}
	}
	return nil
}
