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
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("Scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.GPUPool{}).
		Build()
}

func TestApplyCreatesObjects(t *testing.T) {
	objs, err := BuildManifests(validOptions())
	if err != nil {
		t.Fatal(err)
	}
	c := newFakeClient(t)

	results, err := Apply(t.Context(), c, objs)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %v", len(results), results)
	}
	for _, r := range results {
		if !strings.HasSuffix(r, "created") {
			t.Errorf("result %q should report creation", r)
		}
	}

	var model v1alpha1.GPUModel
	if err := c.Get(t.Context(), types.NamespacedName{Name: testModelName}, &model); err != nil {
		t.Errorf("GPUModel not created: %v", err)
	}
	var pool v1alpha1.GPUPool
	if err := c.Get(t.Context(), types.NamespacedName{Name: testPoolName}, &pool); err != nil {
		t.Errorf("GPUPool not created: %v", err)
	}
}

// Re-running with adjusted flags is the obvious way to change a simulated
// fleet. A CLI that only ever creates would fail on AlreadyExists and force
// users to delete first.
func TestApplyIsIdempotentAndUpdates(t *testing.T) {
	first, err := BuildManifests(validOptions())
	if err != nil {
		t.Fatal(err)
	}
	c := newFakeClient(t)
	if _, err := Apply(t.Context(), c, first); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	opts := validOptions()
	opts.GPUsPerNode = 16
	second, err := BuildManifests(opts)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Apply(t.Context(), c, second)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	for _, r := range results {
		if !strings.HasSuffix(r, "configured") {
			t.Errorf("result %q should report an update on re-apply", r)
		}
	}

	var pool v1alpha1.GPUPool
	if err := c.Get(t.Context(), types.NamespacedName{Name: testPoolName}, &pool); err != nil {
		t.Fatal(err)
	}
	if pool.Spec.GPUsPerNode != 16 {
		t.Errorf("gpusPerNode = %d, want 16; the re-apply did not take effect", pool.Spec.GPUsPerNode)
	}
}

// On a re-apply the pool already carries devices from the previous spec.
// Returning those would make the CLI report the pre-update count: change
// gpusPerNode from 8 to 16 and it would still print the old total. The wait
// must hold until the operator has observed the new generation.
func TestWaitForPoolIgnoresStaleStatus(t *testing.T) {
	pool := &v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName, Generation: 2},
		Spec:       v1alpha1.GPUPoolSpec{ModelRef: testModelName, GPUsPerNode: 16},
		Status: v1alpha1.GPUPoolStatus{
			ObservedGeneration: 1, // still reflects the previous spec
			NodesMatched:       2,
			DevicesPublished:   16,
		},
	}
	c := newFakeClient(t, pool)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()

	if _, err := WaitForPool(ctx, c, testPoolName, 10*time.Millisecond); err == nil {
		t.Error("returned stale status from a generation the operator has not observed yet")
	}
}

func TestWaitForPoolReturnsWhenDevicesPublished(t *testing.T) {
	pool := &v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName, Generation: 3},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef: testModelName, GPUsPerNode: 8,
		},
		Status: v1alpha1.GPUPoolStatus{
			ObservedGeneration: 3,
			NodesMatched:       2,
			DevicesPublished:   16,
		},
	}
	c := newFakeClient(t, pool)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	got, err := WaitForPool(ctx, c, testPoolName, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForPool: %v", err)
	}
	if got.NodesMatched != 2 || got.DevicesPublished != 16 {
		t.Errorf("got %+v, want {NodesMatched:2 DevicesPublished:16}", got)
	}
}

// A pool whose selector matches no simulated node reconciles successfully and
// reports zero forever. The caller needs the last observed report alongside the
// timeout so it can say which of the two happened.
func TestWaitForPoolTimesOutReportingZero(t *testing.T) {
	pool := &v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: testPoolName},
		Spec:       v1alpha1.GPUPoolSpec{ModelRef: testModelName, GPUsPerNode: 8},
	}
	c := newFakeClient(t, pool)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()

	got, err := WaitForPool(ctx, c, testPoolName, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if got.DevicesPublished != 0 {
		t.Errorf("DevicesPublished = %d, want 0", got.DevicesPublished)
	}
}
