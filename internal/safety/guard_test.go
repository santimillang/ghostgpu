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

package safety

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// kwokFake is the annotation value kwok itself uses.
const kwokFake = "fake"

func nodeWith(name string, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
	}
}

func simulated(name string) *corev1.Node {
	return nodeWith(name, map[string]string{KwokNodeAnnotation: kwokFake})
}

func TestIsSimulatedNode(t *testing.T) {
	cases := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "kwok node with conventional value",
			node: nodeWith("n", map[string]string{KwokNodeAnnotation: kwokFake}),
			want: true,
		},
		{
			name: "kwok annotation with any value is still kwok-managed",
			node: nodeWith("n", map[string]string{KwokNodeAnnotation: "something-else"}),
			want: true,
		},
		{
			name: "kwok annotation with empty value counts as present",
			node: nodeWith("n", map[string]string{KwokNodeAnnotation: ""}),
			want: true,
		},
		{
			name: "kwok annotation alongside others",
			node: nodeWith("n", map[string]string{"other": "x", KwokNodeAnnotation: kwokFake}),
			want: true,
		},
		{
			name: "real node with unrelated annotations",
			node: nodeWith("n", map[string]string{"node.alpha.kubernetes.io/ttl": "0"}),
			want: false,
		},
		{
			name: "real node with no annotations",
			node: nodeWith("n", nil),
			want: false,
		},
		{
			name: "real node with empty annotation map",
			node: nodeWith("n", map[string]string{}),
			want: false,
		},
		{
			name: "a similarly-named annotation must not match",
			node: nodeWith("n", map[string]string{"kwok.x-k8s.io/node-group": "fake"}),
			want: false,
		},
		{
			name: "a label of the same name must not match",
			node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   "n",
				Labels: map[string]string{KwokNodeAnnotation: kwokFake},
			}},
			want: false,
		},
		{
			name: "nil node is never simulated",
			node: nil,
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsSimulatedNode(c.node); got != c.want {
				t.Errorf("IsSimulatedNode() = %v, want %v", got, c.want)
			}
		})
	}
}

// FilterSimulated exists so callers cannot forget the guard. If it ever lets a
// real node through, ghostgpu would mutate production infrastructure.
func TestFilterSimulatedExcludesRealNodes(t *testing.T) {
	nodes := []corev1.Node{
		*simulated("fake-1"),
		*nodeWith("real-1", nil),
		*simulated("fake-2"),
		*nodeWith("real-2", map[string]string{"foo": "bar"}),
	}

	got := FilterSimulated(nodes)

	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2: %v", len(got), names(got))
	}
	for _, n := range got {
		if !IsSimulatedNode(&n) {
			t.Errorf("SAFETY VIOLATION: FilterSimulated returned real node %q", n.Name)
		}
	}
}

func TestFilterSimulatedPreservesOrder(t *testing.T) {
	nodes := []corev1.Node{
		*simulated("a"),
		*nodeWith("real", nil),
		*simulated("b"),
		*simulated("c"),
	}

	got := names(FilterSimulated(nodes))
	want := []string{"a", "b", "c"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFilterSimulatedOnAllRealNodesReturnsEmpty(t *testing.T) {
	nodes := []corev1.Node{*nodeWith("real-1", nil), *nodeWith("real-2", nil)}
	if got := FilterSimulated(nodes); len(got) != 0 {
		t.Errorf("SAFETY VIOLATION: got %v from an all-real node list, want none", names(got))
	}
}

func TestFilterSimulatedOnEmptyInput(t *testing.T) {
	if got := FilterSimulated(nil); len(got) != 0 {
		t.Errorf("FilterSimulated(nil) = %v, want empty", names(got))
	}
	if got := FilterSimulated([]corev1.Node{}); len(got) != 0 {
		t.Errorf("FilterSimulated(empty) = %v, want empty", names(got))
	}
}

func names(nodes []corev1.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}
