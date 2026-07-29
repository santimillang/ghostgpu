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

// Package safety enforces ghostgpu's central invariant: it must only ever
// modify simulated infrastructure.
//
// ghostgpu writes to Node objects — capacity, allocatable, and labels. On a
// real node that would be actively harmful: advertising GPUs that do not
// exist causes the scheduler to place workloads the node cannot run. Every
// write path must therefore pass through this package first.
//
// This guards against accident: an operator pointed at the wrong cluster, or
// a nodeSelector matching more broadly than intended. It is not a defense
// against a compromised operator, which would hold the RBAC to bypass it.
// See SECURITY.md for the full threat model.
package safety

import corev1 "k8s.io/api/core/v1"

// KwokNodeAnnotation marks a Node as managed by kwok. Its presence is the
// sole signal that a node is safe for ghostgpu to modify.
const KwokNodeAnnotation = "kwok.x-k8s.io/node"

// IsSimulatedNode reports whether a node is kwok-managed, and therefore safe
// to modify. ghostgpu must never write to a node where this returns false.
//
// Presence of the annotation is the test, not its value. kwok's own
// convention is "fake", but matching on presence keeps ghostgpu working if
// kwok introduces other values. The risk this trades away is negligible:
// nobody annotates a production node with kwok.x-k8s.io/node by accident,
// whereas silently skipping legitimate kwok nodes would make ghostgpu look
// broken for a reason that is hard to diagnose.
func IsSimulatedNode(node *corev1.Node) bool {
	if node == nil {
		return false
	}
	_, ok := node.Annotations[KwokNodeAnnotation]
	return ok
}

// FilterSimulated returns only the nodes that are safe to modify.
//
// Callers should prefer this over calling IsSimulatedNode in a loop: it makes
// the safe path the default one, so a future change cannot accidentally omit
// the check on some branch.
func FilterSimulated(nodes []corev1.Node) []corev1.Node {
	out := make([]corev1.Node, 0, len(nodes))
	for _, n := range nodes {
		if IsSimulatedNode(&n) {
			out = append(out, n)
		}
	}
	return out
}
