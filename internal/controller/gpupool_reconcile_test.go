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

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/mig"
	"github.com/santimillang/ghostgpu/internal/safety"
)

// These tests use a fake client rather than envtest because they assert
// reconciler behaviour, not API server behaviour. The envtest specs in
// gpupool_controller_test.go cover schema installation, defaulting, and
// validation, which a fake client cannot.

const (
	fakeModelName = "h100"
	fakePoolName  = "h100-pool"
	nodeA         = "node-a"
	nodeB         = "node-b"
	gpuResource   = "nvidia.com/gpu"
	productLabel  = "nvidia.com/gpu.product"
	productH100   = "NVIDIA-H100-SXM"

	nodeTypeLabel = "type"
	kwokNodeType  = "kwok"

	sliceNodeA = "h100-pool-node-a"
	sliceNodeB = "h100-pool-node-b"

	computeCapability = "9.0"
	profile1g10gb     = "1g.10gb"

	// A product ghostgpu has a built-in MIG profile table for, unlike
	// productH100 above, whose name predates the built-in tables.
	productH100MIG = "NVIDIA-H100-80GB-HBM3"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilRuntimeMust(clientgoscheme.AddToScheme(s))
	utilRuntimeMust(resourcev1.AddToScheme(s))
	utilRuntimeMust(v1alpha1.AddToScheme(s))
	return s
}

func utilRuntimeMust(err error) {
	if err != nil {
		panic(err)
	}
}

func newReconciler(objs ...client.Object) *GPUPoolReconciler {
	s := testScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.GPUPool{}, &corev1.Node{}).
		Build()
	return &GPUPoolReconciler{Client: c, Scheme: s}
}

func poolReq(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}
}

// simNode is a kwok-managed node: it carries the annotation that marks it as
// safe for ghostgpu to modify.
func simNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      map[string]string{nodeTypeLabel: kwokNodeType},
			Annotations: map[string]string{safety.KwokNodeAnnotation: "fake"},
		},
	}
}

// realNode matches the pool's selector but has no kwok annotation. It stands in
// for a production GPU node in a cluster where ghostgpu was installed by mistake.
func realNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{nodeTypeLabel: kwokNodeType},
		},
	}
}

func fixtures() (*v1alpha1.GPUModel, *v1alpha1.GPUPool) {
	model := &v1alpha1.GPUModel{
		ObjectMeta: metav1.ObjectMeta{Name: fakeModelName, UID: "model-uid"},
		Spec: v1alpha1.GPUModelSpec{
			Vendor:            "nvidia",
			ProductName:       productH100,
			Memory:            resource.MustParse("80Gi"),
			ComputeCapability: computeCapability,
		},
	}
	pool := &v1alpha1.GPUPool{
		ObjectMeta: metav1.ObjectMeta{Name: fakePoolName, UID: "pool-uid", Generation: 3},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef:     fakeModelName,
			NodeSelector: map[string]string{nodeTypeLabel: kwokNodeType},
			GPUsPerNode:  8,
			Advertise:    v1alpha1.AdvertiseSpec{DRA: ptr.To(true), ExtendedResource: ptr.To(true)},
			Topology:     v1alpha1.TopologySpec{NVLinkDomainSize: 4},
		},
	}
	return model, pool
}

func getNode(t *testing.T, r *GPUPoolReconciler, name string) *corev1.Node {
	t.Helper()
	var node corev1.Node
	if err := r.Get(t.Context(), types.NamespacedName{Name: name}, &node); err != nil {
		t.Fatalf("get node %s: %v", name, err)
	}
	return &node
}

func getPool(t *testing.T, r *GPUPoolReconciler) *v1alpha1.GPUPool {
	t.Helper()
	var pool v1alpha1.GPUPool
	if err := r.Get(t.Context(), types.NamespacedName{Name: fakePoolName}, &pool); err != nil {
		t.Fatalf("get pool: %v", err)
	}
	return &pool
}

func reconcileOnce(t *testing.T, r *GPUPoolReconciler) {
	t.Helper()
	if _, err := r.Reconcile(t.Context(), poolReq(fakePoolName)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestReconcilePublishesResourceSlice(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	var slice resourcev1.ResourceSlice
	key := types.NamespacedName{Name: sliceNodeA}
	if err := r.Get(t.Context(), key, &slice); err != nil {
		t.Fatalf("expected ResourceSlice: %v", err)
	}
	if len(slice.Spec.Devices) != 8 {
		t.Errorf("got %d devices, want 8", len(slice.Spec.Devices))
	}
	if slice.Spec.NodeName == nil || *slice.Spec.NodeName != nodeA {
		t.Errorf("slice not bound to %s", nodeA)
	}
}

// Owner references are what make ResourceSlices disappear when the pool is
// deleted. Without them, deleting a GPUPool would leave the scheduler
// allocating devices from a pool that no longer exists.
func TestReconcileSetsOwnerReference(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	var slice resourcev1.ResourceSlice
	if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &slice); err != nil {
		t.Fatal(err)
	}

	owner := metav1.GetControllerOf(&slice)
	if owner == nil {
		t.Fatal("no controller owner reference; slice would leak when the pool is deleted")
	}
	if owner.Kind != kindGPUPool || owner.Name != fakePoolName {
		t.Errorf("owner = %s/%s, want %s/%s", owner.Kind, owner.Name, kindGPUPool, fakePoolName)
	}
}

func TestReconcilePatchesNodeCapacityAndLabels(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	node := getNode(t, r, nodeA)
	if got := node.Status.Capacity[gpuResource]; got.Value() != 8 {
		t.Errorf("capacity %s = %v, want 8", gpuResource, got.Value())
	}
	if got := node.Status.Allocatable[gpuResource]; got.Value() != 8 {
		t.Errorf("allocatable %s = %v, want 8", gpuResource, got.Value())
	}
	if node.Labels[productLabel] != productH100 {
		t.Errorf("missing GFD product label: %v", node.Labels)
	}
	if node.Labels["type"] != "kwok" {
		t.Error("pre-existing node labels were dropped")
	}
}

// The safety invariant from design spec §5: a node without the kwok annotation
// must never be modified. This is the single test that stands between a
// misconfigured install and a mutated production node.
func TestReconcileRefusesRealNodes(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, realNode("real-node"))

	reconcileOnce(t, r)

	node := getNode(t, r, "real-node")
	if _, touched := node.Status.Capacity[gpuResource]; touched {
		t.Error("SAFETY VIOLATION: real node capacity was modified")
	}
	if _, touched := node.Labels["nvidia.com/gpu.present"]; touched {
		t.Error("SAFETY VIOLATION: real node labels were modified")
	}

	var slice resourcev1.ResourceSlice
	err := r.Get(t.Context(), types.NamespacedName{Name: "h100-pool-real-node"}, &slice)
	if err == nil {
		t.Error("SAFETY VIOLATION: ResourceSlice published for a real node")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected error: %v", err)
	}

	if pool := getPool(t, r); pool.Status.NodesMatched != 0 {
		t.Errorf("NodesMatched = %d, want 0; a real node was counted", pool.Status.NodesMatched)
	}
}

func TestReconcileReportsStatus(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, simNode(nodeA), simNode(nodeB))

	reconcileOnce(t, r)

	got := getPool(t, r)
	if got.Status.NodesMatched != 2 {
		t.Errorf("NodesMatched = %d, want 2", got.Status.NodesMatched)
	}
	if got.Status.DevicesPublished != 16 {
		t.Errorf("DevicesPublished = %d, want 16", got.Status.DevicesPublished)
	}
	if got.Status.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration = %d, want 3", got.Status.ObservedGeneration)
	}

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not set")
	}
	if ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True (reason %q)", ready.Status, ready.Reason)
	}
}

// A dangling GPUModel reference is a user error, not an operator error. It must
// surface on the pool's status rather than hot-looping the reconciler.
func TestReconcileReportsMissingModel(t *testing.T) {
	_, pool := fixtures()
	pool.Spec.ModelRef = "does-not-exist"
	r := newReconciler(pool, simNode(nodeA))

	reconcileOnce(t, r)

	ready := meta.FindStatusCondition(getPool(t, r).Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not set")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %s, want False", ready.Status)
	}
	if ready.Reason != "ModelNotFound" {
		t.Errorf("Reason = %q, want ModelNotFound", ready.Reason)
	}
}

// A pool asking for MIG on a GPU that cannot be partitioned spans two objects,
// so the API server cannot reject it. It must surface on status rather than
// producing a pool that silently simulates nothing.
func TestReconcileRejectsMIGOnUnpartitionableModel(t *testing.T) {
	model, pool := fixtures()
	model.Spec.ProductName = "NVIDIA-GTX-1080" // no built-in MIG table
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	ready := meta.FindStatusCondition(getPool(t, r).Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not set")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %s, want False", ready.Status)
	}
	if ready.Reason != "MIGProfilesInvalid" {
		t.Errorf("Reason = %q, want MIGProfilesInvalid", ready.Reason)
	}

	// Nothing may be published for a pool that failed validation.
	var slice resourcev1.ResourceSlice
	if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &slice); err == nil {
		t.Error("a ResourceSlice was published for an invalid MIG pool")
	}
}

// A MIG pool on hardware ghostgpu knows must pass validation and keep
// reconciling. Until slice construction lands it still publishes whole GPUs.
func TestReconcileAcceptsMIGOnKnownModel(t *testing.T) {
	model, pool := fixtures()
	model.Spec.ProductName = productH100MIG
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	ready := meta.FindStatusCondition(getPool(t, r).Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not set")
	}
	if ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True (reason %q, message %q)",
			ready.Status, ready.Reason, ready.Message)
	}
}

// migFixtures returns a pool partitioned into MIG instances on hardware
// ghostgpu has a built-in profile table for.
func migFixtures() (*v1alpha1.GPUModel, *v1alpha1.GPUPool) {
	model, pool := fixtures()
	model.Spec.ProductName = productH100MIG
	pool.Spec.SharingMode = v1alpha1.SharingModeMIG
	return model, pool
}

// h100ProfileCount is how many profiles the built-in H100 table carries. Read
// rather than hardcoded, so adding a profile does not silently break the
// arithmetic these tests assert.
func h100ProfileCount(t *testing.T) int32 {
	t.Helper()
	table, ok := mig.ProfilesFor(productH100MIG)
	if !ok {
		t.Fatal("no built-in H100 table")
	}
	return int32(len(table.Profiles))
}

func listSlices(t *testing.T, r *GPUPoolReconciler) []resourcev1.ResourceSlice {
	t.Helper()
	var slices resourcev1.ResourceSliceList
	if err := r.List(t.Context(), &slices); err != nil {
		t.Fatalf("list slices: %v", err)
	}
	return slices.Items
}

func TestReconcilePublishesMIGSlices(t *testing.T) {
	model, pool := migFixtures()
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	slices := listSlices(t, r)
	// 8 GPUs x 6 profiles = 48 devices: one counter slice, one device slice.
	if len(slices) != 2 {
		t.Fatalf("got %d slices, want 2 (1 counter + 1 device)", len(slices))
	}

	var counters, devices int
	for i := range slices {
		s := &slices[i]
		counters += len(s.Spec.SharedCounters)
		devices += len(s.Spec.Devices)

		if s.Labels[PoolLabel] != fakePoolName {
			t.Errorf("slice %s missing the pool label; pruning would never find it", s.Name)
		}
		if owner := metav1.GetControllerOf(s); owner == nil || owner.Name != fakePoolName {
			t.Errorf("slice %s has no controller owner reference", s.Name)
		}
	}

	if counters != 8 {
		t.Errorf("got %d counter sets, want one per GPU (8)", counters)
	}
	if want := 8 * int(h100ProfileCount(t)); devices != want {
		t.Errorf("got %d devices, want %d", devices, want)
	}

	// The whole-GPU slice must not also exist, or each card would be
	// allocatable both whole and in pieces at the same time.
	for i := range slices {
		if slices[i].Name == sliceNodeA {
			t.Error("the whole-GPU slice was published alongside MIG instances")
		}
	}
}

func TestReconcileReportsMIGStatus(t *testing.T) {
	model, pool := migFixtures()
	r := newReconciler(model, pool, simNode(nodeA), simNode(nodeB))

	reconcileOnce(t, r)

	got := getPool(t, r)
	profiles := h100ProfileCount(t)

	if got.Status.NodesMatched != 2 {
		t.Errorf("NodesMatched = %d, want 2", got.Status.NodesMatched)
	}
	if want := 2 * 8 * profiles; got.Status.DevicesPublished != want {
		t.Errorf("DevicesPublished = %d, want %d (2 nodes x 8 GPUs x %d profiles)",
			got.Status.DevicesPublished, want, profiles)
	}
	if got.Status.MIGProfilesPublished != profiles {
		t.Errorf("MIGProfilesPublished = %d, want %d", got.Status.MIGProfilesPublished, profiles)
	}
}

// Switching a live pool between sharing modes is the case multi-slice pools
// make easy to get wrong: the old objects are numerous and none of them share
// a name with the new ones, so anything left behind keeps advertising devices
// that no longer exist.
func TestReconcilePrunesAcrossSharingModeSwitch(t *testing.T) {
	t.Run("mig to none", func(t *testing.T) {
		model, pool := migFixtures()
		r := newReconciler(model, pool, simNode(nodeA))
		reconcileOnce(t, r)

		if len(listSlices(t, r)) != 2 {
			t.Fatalf("setup: expected 2 MIG slices, got %d", len(listSlices(t, r)))
		}

		live := getPool(t, r)
		live.Spec.SharingMode = v1alpha1.SharingModeNone
		if err := r.Update(t.Context(), live); err != nil {
			t.Fatalf("update pool: %v", err)
		}
		reconcileOnce(t, r)

		slices := listSlices(t, r)
		if len(slices) != 1 {
			t.Fatalf("got %d slices after switching to none, want 1 whole-GPU slice", len(slices))
		}
		if slices[0].Name != sliceNodeA {
			t.Errorf("surviving slice is %q, want %q", slices[0].Name, sliceNodeA)
		}
		if len(slices[0].Spec.SharedCounters) != 0 {
			t.Error("a counter slice survived the switch away from MIG")
		}
		if got := getPool(t, r).Status.MIGProfilesPublished; got != 0 {
			t.Errorf("MIGProfilesPublished = %d, want 0 after leaving MIG", got)
		}
	})

	t.Run("none to mig", func(t *testing.T) {
		model, pool := fixtures()
		model.Spec.ProductName = productH100MIG
		r := newReconciler(model, pool, simNode(nodeA))
		reconcileOnce(t, r)

		if len(listSlices(t, r)) != 1 {
			t.Fatalf("setup: expected 1 whole-GPU slice, got %d", len(listSlices(t, r)))
		}

		live := getPool(t, r)
		live.Spec.SharingMode = v1alpha1.SharingModeMIG
		if err := r.Update(t.Context(), live); err != nil {
			t.Fatalf("update pool: %v", err)
		}
		reconcileOnce(t, r)

		for _, s := range listSlices(t, r) {
			if s.Name == sliceNodeA {
				t.Error("the whole-GPU slice survived the switch to MIG")
			}
		}
	})
}

// The safety invariant holds on the MIG path too. It is enforced before any
// slice is built, but a separate assertion keeps it from regressing when only
// the MIG branch changes.
func TestReconcileMIGRefusesRealNodes(t *testing.T) {
	model, pool := migFixtures()
	r := newReconciler(model, pool, realNode("real-node"))

	reconcileOnce(t, r)

	if slices := listSlices(t, r); len(slices) != 0 {
		t.Errorf("SAFETY VIOLATION: %d MIG slices published for an unmanaged node", len(slices))
	}
	node := getNode(t, r, "real-node")
	if _, touched := node.Status.Capacity[gpuResource]; touched {
		t.Error("SAFETY VIOLATION: unmanaged node capacity was modified")
	}
}

func TestReconcileMIGIsIdempotent(t *testing.T) {
	model, pool := migFixtures()
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)
	first := listSlices(t, r)

	reconcileOnce(t, r)
	second := listSlices(t, r)

	if len(first) != len(second) {
		t.Fatalf("slice count changed on a no-op reconcile: %d -> %d", len(first), len(second))
	}
	versions := map[string]string{}
	for i := range first {
		versions[first[i].Name] = first[i].ResourceVersion
	}
	for i := range second {
		if was, ok := versions[second[i].Name]; ok && was != second[i].ResourceVersion {
			t.Errorf("slice %s rewritten on a no-op reconcile: %s -> %s",
				second[i].Name, was, second[i].ResourceVersion)
		}
	}
}

// kwok nodes are created and destroyed constantly during a simulation. A slice
// left behind for a departed node would advertise GPUs on a node that no longer
// exists, and the scheduler would keep trying to place pods there.
func TestReconcilePrunesSlicesForDepartedNodes(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, simNode(nodeA), simNode(nodeB))

	reconcileOnce(t, r)

	if err := r.Delete(t.Context(), simNode(nodeB)); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	reconcileOnce(t, r)

	var slice resourcev1.ResourceSlice
	err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeB}, &slice)
	if err == nil {
		t.Error("stale ResourceSlice survived; it advertises GPUs on a deleted node")
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &slice); err != nil {
		t.Errorf("pruning removed a live node's slice: %v", err)
	}
	if got := getPool(t, r).Status.NodesMatched; got != 1 {
		t.Errorf("NodesMatched = %d, want 1", got)
	}
}

// Slices belonging to another pool must survive, or two pools would fight and
// alternately delete each other's devices on every reconcile.
func TestReconcileDoesNotPruneOtherPoolsSlices(t *testing.T) {
	model, pool := fixtures()
	foreign := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "other-pool-node-z",
			Labels: map[string]string{PoolLabel: "other-pool"},
		},
		Spec: resourcev1.ResourceSliceSpec{
			Driver: "gpu.ghostgpu.dev",
			Pool:   resourcev1.ResourcePool{Name: "node-z", ResourceSliceCount: 1},
		},
	}
	r := newReconciler(model, pool, simNode(nodeA), foreign)

	reconcileOnce(t, r)

	var slice resourcev1.ResourceSlice
	if err := r.Get(t.Context(), types.NamespacedName{Name: "other-pool-node-z"}, &slice); err != nil {
		t.Errorf("another pool's ResourceSlice was pruned: %v", err)
	}
}

// A pool built in Go without touching Advertise leaves both fields nil. The
// operator must read that as "enabled", matching the CRD default, or an
// in-process caller would get a pool that advertises nothing.
func TestReconcileTreatsUnsetAdvertiseAsEnabled(t *testing.T) {
	model, pool := fixtures()
	pool.Spec.Advertise = v1alpha1.AdvertiseSpec{}
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)

	var slice resourcev1.ResourceSlice
	if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &slice); err != nil {
		t.Errorf("no ResourceSlice for a pool with unset advertise: %v", err)
	}
	if got := getNode(t, r, nodeA).Status.Capacity[gpuResource]; got.Value() != 8 {
		t.Errorf("capacity = %v, want 8 for a pool with unset advertise", got.Value())
	}
}

func TestReconcileRespectsAdvertiseToggles(t *testing.T) {
	t.Run("dra disabled publishes no slice", func(t *testing.T) {
		model, pool := fixtures()
		pool.Spec.Advertise.DRA = ptr.To(false)
		r := newReconciler(model, pool, simNode(nodeA))

		reconcileOnce(t, r)

		var slice resourcev1.ResourceSlice
		if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &slice); err == nil {
			t.Error("ResourceSlice published despite advertise.dra=false")
		}
		if got := getNode(t, r, nodeA).Status.Capacity[gpuResource]; got.Value() != 8 {
			t.Error("extended resource should still be advertised")
		}
	})

	t.Run("extended resource disabled leaves capacity alone", func(t *testing.T) {
		model, pool := fixtures()
		pool.Spec.Advertise.ExtendedResource = ptr.To(false)
		r := newReconciler(model, pool, simNode(nodeA))

		reconcileOnce(t, r)

		if _, ok := getNode(t, r, nodeA).Status.Capacity[gpuResource]; ok {
			t.Error("node capacity patched despite advertise.extendedResource=false")
		}
		var slice resourcev1.ResourceSlice
		if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &slice); err != nil {
			t.Errorf("DRA slice should still be published: %v", err)
		}
	})
}

// Reconcile runs constantly. Converging to the same state twice must not
// produce a different result, or every resync would churn the API server.
func TestReconcileIsIdempotent(t *testing.T) {
	model, pool := fixtures()
	r := newReconciler(model, pool, simNode(nodeA))

	reconcileOnce(t, r)
	var first resourcev1.ResourceSlice
	if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &first); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r)
	var second resourcev1.ResourceSlice
	if err := r.Get(t.Context(), types.NamespacedName{Name: sliceNodeA}, &second); err != nil {
		t.Fatal(err)
	}

	if first.ResourceVersion != second.ResourceVersion {
		t.Errorf("slice rewritten on a no-op reconcile: %s -> %s",
			first.ResourceVersion, second.ResourceVersion)
	}
}

// A deleted pool must not be recreated or error; the request simply drains.
func TestReconcileMissingPoolIsNotAnError(t *testing.T) {
	r := newReconciler()

	if _, err := r.Reconcile(t.Context(), poolReq("gone")); err != nil {
		t.Errorf("Reconcile on a deleted pool returned %v, want nil", err)
	}
}
