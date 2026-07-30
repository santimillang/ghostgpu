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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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

	// GPUResourceName is the legacy extended resource ghostgpu advertises
	// alongside DRA, for schedulers and tooling that predate it.
	GPUResourceName corev1.ResourceName = "nvidia.com/gpu"
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
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var model ghostgpuv1alpha1.GPUModel
	if err := r.Get(ctx, client.ObjectKey{Name: pool.Spec.ModelRef}, &model); err != nil {
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
	if pool.Spec.MIGEnabled() {
		if _, err := mig.Resolve(&model); err != nil {
			setReady(&pool, metav1.ConditionFalse, "MIGProfilesInvalid", err.Error())
			return ctrl.Result{}, r.Status().Update(ctx, &pool)
		}
	}

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes, client.MatchingLabels(pool.Spec.NodeSelector)); err != nil {
		return ctrl.Result{}, err
	}

	var matched, devices int32
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

		if pool.Spec.Advertise.DRAEnabled() {
			if err := r.reconcileSlice(ctx, &pool, &model, node.Name); err != nil {
				return ctrl.Result{}, err
			}
			live[gpu.SliceName(pool.Name, node.Name)] = struct{}{}
			devices += pool.Spec.GPUsPerNode
		}

		if err := r.reconcileNode(ctx, &pool, &model, node); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.pruneSlices(ctx, &pool, live); err != nil {
		return ctrl.Result{}, err
	}

	pool.Status.ObservedGeneration = pool.Generation
	pool.Status.NodesMatched = matched
	pool.Status.DevicesPublished = devices
	setReady(&pool, metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("simulated %d GPUs across %d nodes", devices, matched))

	return ctrl.Result{}, r.Status().Update(ctx, &pool)
}

// reconcileSlice creates or updates the ResourceSlice advertising one node's
// GPUs. BuildResourceSlice is a pure function, so an unchanged pool produces an
// identical spec and CreateOrUpdate issues no write.
func (r *GPUPoolReconciler) reconcileSlice(
	ctx context.Context,
	pool *ghostgpuv1alpha1.GPUPool,
	model *ghostgpuv1alpha1.GPUModel,
	nodeName string,
) error {
	desired := gpu.BuildResourceSlice(pool, model, nodeName)

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
	node *corev1.Node,
) error {
	if pool.Spec.Advertise.ExtendedResourceEnabled() {
		want := *resource.NewQuantity(int64(pool.Spec.GPUsPerNode), resource.DecimalSI)

		if !hasQuantity(node.Status.Capacity, want) || !hasQuantity(node.Status.Allocatable, want) {
			patched := node.DeepCopy()
			if patched.Status.Capacity == nil {
				patched.Status.Capacity = corev1.ResourceList{}
			}
			if patched.Status.Allocatable == nil {
				patched.Status.Allocatable = corev1.ResourceList{}
			}
			patched.Status.Capacity[GPUResourceName] = want
			patched.Status.Allocatable[GPUResourceName] = want

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

// hasQuantity reports whether the list already advertises want for the GPU
// extended resource. An absent entry never counts as a match, so a node that
// has never been patched is always updated.
func hasQuantity(list corev1.ResourceList, want resource.Quantity) bool {
	got, ok := list[GPUResourceName]
	return ok && got.Cmp(want) == 0
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
