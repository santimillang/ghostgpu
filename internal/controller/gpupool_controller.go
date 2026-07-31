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
	"context"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ghostgpuv1alpha1 "github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
	"github.com/santimillang/ghostgpu/internal/mig"
	"github.com/santimillang/ghostgpu/internal/safety"
)

const (
	// ConditionReady reports whether a pool's simulated GPUs are published.
	ConditionReady = "Ready"

	// PoolLabel marks a ResourceSlice as managed by a named GPUPool.
	//
	// Owner references make slices disappear when their pool is deleted, but
	// garbage collection cannot help when a *node* goes away and the pool
	// survives. This label is how a reconcile finds the slices it owns so it
	// can prune the ones no longer backed by a node.
	PoolLabel = "ghostgpu.dev/pool"
)

// GPUPoolReconciler reconciles a GPUPool object.
type GPUPoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ghostgpu.dev,resources=gpupools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ghostgpu.dev,resources=gpupools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ghostgpu.dev,resources=gpupools/finalizers,verbs=update
// +kubebuilder:rbac:groups=ghostgpu.dev,resources=gpumodels,verbs=get;list;watch
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceslices,verbs=get;list;watch;create;update;patch;delete
// ResourceClaims are read-only and are not this controller's concern: the
// metrics exporter needs them to attribute a simulated GPU to the pod holding
// it. Allocation is the scheduler's to write, and ghostgpu never does.
// +kubebuilder:rbac:groups=resource.k8s.io,resources=resourceclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch

// Reconcile publishes one node's worth of simulated GPUs for every kwok node
// matching the pool's selector: a DRA ResourceSlice, legacy extended-resource
// capacity, and GPU Feature Discovery labels.
//
// Nodes that are not kwok-managed are skipped unconditionally (see the safety
// package). That check is the reason this operator is safe to install in a
// cluster that also contains real GPU hardware.
func (r *GPUPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var pool ghostgpuv1alpha1.GPUPool
	err := r.Get(ctx, req.NamespacedName, &pool)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var model ghostgpuv1alpha1.GPUModel
	if err = r.Get(ctx, client.ObjectKey{Name: pool.Spec.ModelRef}, &model); err != nil {
		if apierrors.IsNotFound(err) {
			// A dangling modelRef is a user error. Reporting it on status is
			// more useful than returning an error, which would only retry a
			// reference that cannot resolve until the user acts.
			setReady(&pool, metav1.ConditionFalse, "ModelNotFound",
				fmt.Sprintf("GPUModel %q not found", pool.Spec.ModelRef))
			return ctrl.Result{}, r.Status().Update(ctx, &pool)
		}
		return ctrl.Result{}, err
	}

	// A pool asking for MIG on a model that cannot be partitioned is a user
	// error the API server cannot catch: it spans two objects. Surface it on
	// status rather than publishing a pool that simulates nothing.
	var table mig.Table
	if pool.Spec.MIGEnabled() {
		table, err = mig.Resolve(&model)
		if err != nil {
			setReady(&pool, metav1.ConditionFalse, "MIGProfilesInvalid", err.Error())
			return ctrl.Result{}, r.Status().Update(ctx, &pool)
		}

		// A partition that cannot fit one card is worse than an unschedulable
		// profile. It is a claim about what the hardware is carrying, so
		// publishing it would advertise capacity nothing can satisfy — the
		// exact failure this feature exists to remove.
		if err = mig.ValidatePartition(pool.Spec.MIGPartition, table); err != nil {
			setReady(&pool, metav1.ConditionFalse, "MIGPartitionInvalid", err.Error())
			return ctrl.Result{}, r.Status().Update(ctx, &pool)
		}
	}

	// Declared occupancy larger than the hardware carries is a claim about a
	// fleet that does not exist. Simulating the nearest one that does would
	// hide the mistake inside the very scenario the user is reasoning about.
	if err := gpu.ValidateOccupancy(&pool); err != nil {
		setReady(&pool, metav1.ConditionFalse, "OccupancyInvalid", err.Error())
		return ctrl.Result{}, r.Status().Update(ctx, &pool)
	}

	if err := gpu.ValidateFaults(&pool); err != nil {
		setReady(&pool, metav1.ConditionFalse, "FaultsInvalid", err.Error())
		return ctrl.Result{}, r.Status().Update(ctx, &pool)
	}

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes, client.MatchingLabels(pool.Spec.NodeSelector)); err != nil {
		return ctrl.Result{}, err
	}

	var matched, devices, occupied, faulted int32
	live := make(map[string]struct{}, len(nodes.Items))

	for i := range nodes.Items {
		node := &nodes.Items[i]

		// Hard safety invariant (design spec §5): never touch a node that is
		// not simulated, whatever the selector matched.
		if !safety.IsSimulatedNode(node) {
			logger.V(1).Info("skipping non-simulated node", "node", node.Name)
			continue
		}
		matched++

		// Resolved per node, because a fleet that is evenly full or evenly
		// broken is not an interesting scenario.
		state := gpu.StateFor(&pool, node)
		occupied += state.Busy
		faulted += state.Faulted

		if pool.Spec.Advertise.DRAEnabled() {
			published, err := r.reconcileSlices(ctx, &pool, &model, table, node.Name, state, live)
			if err != nil {
				return ctrl.Result{}, err
			}
			devices += published
		}

		if err := r.reconcileNode(ctx, &pool, &model, table, node, state); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Pruning matters more under MIG than it ever did for whole GPUs. Switching
	// sharing modes replaces every slice with differently named ones, so
	// anything left behind keeps advertising devices that no longer exist.
	if err := r.pruneSlices(ctx, &pool, live); err != nil {
		return ctrl.Result{}, err
	}

	pool.Status.ObservedGeneration = pool.Generation
	pool.Status.NodesMatched = matched
	pool.Status.DevicesPublished = devices
	pool.Status.GPUsOccupied = occupied
	pool.Status.GPUsFaulted = faulted
	pool.Status.MIGProfilesPublished = int32(len(table.Profiles))
	setReady(&pool, metav1.ConditionTrue, "Reconciled",
		summarize(&pool, devices, matched, occupied, faulted, table))

	return ctrl.Result{}, r.Status().Update(ctx, &pool)
}

func summarize(pool *ghostgpuv1alpha1.GPUPool, devices, matched, occupied, faulted int32, table mig.Table) string {
	summary := fmt.Sprintf("simulated %d GPUs across %d nodes", devices, matched)
	if pool.Spec.MIGEnabled() {
		summary = fmt.Sprintf("simulated %d MIG instances across %d nodes (%d profiles per GPU)",
			devices, matched, len(table.Profiles))
	}
	if occupied > 0 {
		summary += fmt.Sprintf("; %d GPUs declared occupied", occupied)
	}
	if faulted > 0 {
		summary += fmt.Sprintf("; %d GPUs declared faulted", faulted)
	}
	return summary
}

// reconcileSlices publishes one node's DRA slices and records their names as
// live, returning how many devices they advertise.
//
// The whole-GPU and MIG layouts are mutually exclusive: a card offered both
// whole and in pieces at the same time could be allocated twice over.
func (r *GPUPoolReconciler) reconcileSlices(
	ctx context.Context,
	pool *ghostgpuv1alpha1.GPUPool,
	model *ghostgpuv1alpha1.GPUModel,
	table mig.Table,
	nodeName string,
	state gpu.NodeState,
	live map[string]struct{},
) (int32, error) {
	if !pool.Spec.MIGEnabled() {
		if err := r.applySlice(ctx, pool, gpu.BuildResourceSlice(pool, model, nodeName, state)); err != nil {
			return 0, err
		}
		live[gpu.SliceName(pool.Name, nodeName)] = struct{}{}
		return pool.Spec.GPUsPerNode, nil
	}

	var devices int32
	for _, desired := range gpu.BuildMIGSlices(pool, model, table, nodeName, state) {
		if err := r.applySlice(ctx, pool, desired); err != nil {
			return 0, err
		}
		live[desired.Name] = struct{}{}
		devices += int32(len(desired.Spec.Devices))
	}
	return devices, nil
}

// applySlice creates or updates one ResourceSlice. The builders are pure
// functions, so an unchanged pool produces an identical spec and CreateOrUpdate
// issues no write.
func (r *GPUPoolReconciler) applySlice(
	ctx context.Context,
	pool *ghostgpuv1alpha1.GPUPool,
	desired *resourcev1.ResourceSlice,
) error {
	slice := &resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: desired.Name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, slice, func() error {
		slice.Spec = desired.Spec
		if slice.Labels == nil {
			slice.Labels = map[string]string{}
		}
		slice.Labels[PoolLabel] = pool.Name
		return controllerutil.SetControllerReference(pool, slice, r.Scheme)
	})
	return err
}

// pruneSlices deletes ResourceSlices this pool owns that are no longer backed
// by a matching node. kwok nodes are created and destroyed constantly; a slice
// left behind would advertise GPUs on a node that no longer exists, and the
// scheduler would keep placing pods against it.
func (r *GPUPoolReconciler) pruneSlices(
	ctx context.Context,
	pool *ghostgpuv1alpha1.GPUPool,
	live map[string]struct{},
) error {
	var slices resourcev1.ResourceSliceList
	if err := r.List(ctx, &slices, client.MatchingLabels{PoolLabel: pool.Name}); err != nil {
		return err
	}

	for i := range slices.Items {
		slice := &slices.Items[i]
		if _, keep := live[slice.Name]; keep {
			continue
		}
		if err := r.Delete(ctx, slice); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// reconcileNode patches extended-resource capacity and GFD labels onto a
// simulated node.
//
// Both patches are skipped when the node already carries the desired values.
// At the thousand-node scale ghostgpu targets, an unconditional write per node
// per resync is the difference between a quiet cluster and a hot API server.
func (r *GPUPoolReconciler) reconcileNode(
	ctx context.Context,
	pool *ghostgpuv1alpha1.GPUPool,
	model *ghostgpuv1alpha1.GPUModel,
	table mig.Table,
	node *corev1.Node,
	state gpu.NodeState,
) error {
	if pool.Spec.Advertise.ExtendedResourceEnabled() {
		// Capacity is what the hardware has; allocatable is what is left for
		// this scheduler once declared occupancy is taken out. Keeping the two
		// apart is the honest encoding rather than a trick: it is exactly what
		// the distinction means for cpu and memory under --system-reserved.
		capacity := gpu.NodeResources(pool, table)
		allocatable := gpu.NodeAllocatable(pool, table, state)

		if !resourcesMatch(node.Status.Capacity, capacity) ||
			!resourcesMatch(node.Status.Allocatable, allocatable) {
			patched := node.DeepCopy()
			patched.Status.Capacity = applyGPUResources(patched.Status.Capacity, capacity)
			patched.Status.Allocatable = applyGPUResources(patched.Status.Allocatable, allocatable)

			if err := r.Status().Patch(ctx, patched, client.MergeFrom(node)); err != nil {
				return err
			}
			node = patched
		}
	}

	desired := gpu.NodeLabels(pool, model)
	if labelsMatch(node.Labels, desired) {
		return nil
	}

	patched := node.DeepCopy()
	if patched.Labels == nil {
		patched.Labels = map[string]string{}
	}
	maps.Copy(patched.Labels, desired)
	return r.Patch(ctx, patched, client.MergeFrom(node))
}

// managesResource reports whether a resource is one ghostgpu advertises, and so
// one it is responsible for removing when it should no longer be there.
//
// Scoping by prefix rather than by an exact set is what makes a sharing-mode
// switch safe: the resources published before the switch are not known to the
// code running after it, but they are all under this prefix.
func managesResource(name corev1.ResourceName) bool {
	return name == gpu.GPUResourceName || strings.HasPrefix(string(name), gpu.MIGResourcePrefix)
}

// resourcesMatch reports whether the node already advertises exactly the
// desired GPU resources, with no stale ones left over.
//
// The staleness half matters as much as the presence half. Switching a pool to
// MIG replaces nvidia.com/gpu with per-profile resources, and a node that kept
// its old whole-GPU count would advertise capacity that nothing can satisfy.
func resourcesMatch(have, want corev1.ResourceList) bool {
	for name, wantQty := range want {
		haveQty, ok := have[name]
		if !ok || haveQty.Cmp(wantQty) != 0 {
			return false
		}
	}
	for name := range have {
		if managesResource(name) {
			if _, expected := want[name]; !expected {
				return false
			}
		}
	}
	return true
}

// applyGPUResources returns list with ghostgpu's resources set to want and any
// other ghostgpu-managed resource removed. Resources it does not manage, such
// as cpu and memory, are left untouched.
func applyGPUResources(list, want corev1.ResourceList) corev1.ResourceList {
	out := corev1.ResourceList{}
	for name, qty := range list {
		if !managesResource(name) {
			out[name] = qty
		}
	}
	maps.Copy(out, want)
	return out
}

// labelsMatch reports whether every desired label is already present with the
// desired value. Labels the operator does not manage are ignored.
func labelsMatch(have, desired map[string]string) bool {
	for k, v := range desired {
		if have[k] != v {
			return false
		}
	}
	return true
}

func setReady(pool *ghostgpuv1alpha1.GPUPool, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:               ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: pool.Generation,
	})
}

// SetupWithManager wires the controller. Node events re-enqueue every pool,
// because a newly created kwok node may match any pool's selector and the node
// itself carries no reference back to the pools that select it.
func (r *GPUPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ghostgpuv1alpha1.GPUPool{}).
		Named("gpupool").
		Owns(&resourcev1.ResourceSlice{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.poolsForNode)).
		Complete(r)
}

func (r *GPUPoolReconciler) poolsForNode(ctx context.Context, _ client.Object) []ctrl.Request {
	var pools ghostgpuv1alpha1.GPUPoolList
	if err := r.List(ctx, &pools); err != nil {
		logf.FromContext(ctx).Error(err, "listing pools for node event")
		return nil
	}

	reqs := make([]ctrl.Request, 0, len(pools.Items))
	for i := range pools.Items {
		reqs = append(reqs, ctrl.Request{
			NamespacedName: client.ObjectKey{Name: pools.Items[i].Name},
		})
	}
	return reqs
}
