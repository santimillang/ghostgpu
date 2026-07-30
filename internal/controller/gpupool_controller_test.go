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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ghostgpuv1alpha1 "github.com/santimillang/ghostgpu/api/v1alpha1"
)

// These specs run against a real API server via envtest, so they verify what
// unit tests with a fake client cannot: that the generated CRD schema installs,
// validates, and applies defaults exactly as declared.
var _ = Describe("GPUPool", func() {
	const (
		modelName = "test-h100"
		poolName  = "test-pool"
	)

	ctx := context.Background()
	modelKey := types.NamespacedName{Name: modelName}
	poolKey := types.NamespacedName{Name: poolName}

	BeforeEach(func() {
		By("creating a GPUModel")
		model := &ghostgpuv1alpha1.GPUModel{
			ObjectMeta: metav1.ObjectMeta{Name: modelName},
			Spec: ghostgpuv1alpha1.GPUModelSpec{
				Vendor:            "nvidia",
				ProductName:       "NVIDIA-H100-SXM",
				Memory:            resource.MustParse("80Gi"),
				ComputeCapability: "9.0",
			},
		}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, model))).To(Succeed())

		By("creating a GPUPool referencing it")
		pool := &ghostgpuv1alpha1.GPUPool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName},
			Spec: ghostgpuv1alpha1.GPUPoolSpec{
				ModelRef:     modelName,
				NodeSelector: map[string]string{nodeTypeLabel: kwokNodeType},
				GPUsPerNode:  8,
				Topology:     ghostgpuv1alpha1.TopologySpec{NVLinkDomainSize: 4},
			},
		}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, pool))).To(Succeed())
	})

	AfterEach(func() {
		pool := &ghostgpuv1alpha1.GPUPool{ObjectMeta: metav1.ObjectMeta{Name: poolName}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pool))).To(Succeed())

		model := &ghostgpuv1alpha1.GPUModel{ObjectMeta: metav1.ObjectMeta{Name: modelName}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, model))).To(Succeed())
	})

	It("round-trips through the API server", func() {
		fetched := &ghostgpuv1alpha1.GPUPool{}
		Expect(k8sClient.Get(ctx, poolKey, fetched)).To(Succeed())

		Expect(fetched.Spec.ModelRef).To(Equal(modelName))
		Expect(fetched.Spec.GPUsPerNode).To(Equal(int32(8)))
		Expect(fetched.Spec.Topology.NVLinkDomainSize).To(Equal(int32(4)))
	})

	It("defaults both advertise paths to enabled", func() {
		fetched := &ghostgpuv1alpha1.GPUPool{}
		Expect(k8sClient.Get(ctx, poolKey, fetched)).To(Succeed())

		Expect(fetched.Spec.Advertise.DRAEnabled()).To(BeTrue(), "DRA should default to true")
		Expect(fetched.Spec.Advertise.ExtendedResourceEnabled()).To(BeTrue(), "extendedResource should default to true")
	})

	// Regression: a Go client always serializes advertise as at least `{}`, so
	// the nested defaults apply and the bug below stays hidden. A manifest that
	// omits the key entirely gives the API server nothing to default into.
	// Before spec.advertise carried its own `default={}`, such a pool resolved
	// to dra=false and extendedResource=false and advertised nothing at all.
	It("defaults advertise when a manifest omits the key entirely", func() {
		raw := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "ghostgpu.dev/v1alpha1",
			"kind":       "GPUPool",
			"metadata":   map[string]any{"name": "no-advertise-key"},
			"spec": map[string]any{
				"modelRef":    modelName,
				"gpusPerNode": int64(4),
			},
		}}
		Expect(k8sClient.Create(ctx, raw)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, raw))).To(Succeed())
		})

		fetched := &ghostgpuv1alpha1.GPUPool{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "no-advertise-key"}, fetched)).To(Succeed())

		Expect(fetched.Spec.Advertise.DRA).NotTo(BeNil(), "dra was not defaulted")
		Expect(*fetched.Spec.Advertise.DRA).To(BeTrue())
		Expect(fetched.Spec.Advertise.ExtendedResource).NotTo(BeNil(), "extendedResource was not defaulted")
		Expect(*fetched.Spec.Advertise.ExtendedResource).To(BeTrue())
	})

	// The mirror image: an explicit false must survive defaulting. With a plain
	// bool and omitempty, false serialized as absent and came straight back as
	// true, making the field impossible to turn off from any Go client.
	It("preserves an explicitly disabled advertise path", func() {
		off := &ghostgpuv1alpha1.GPUPool{
			ObjectMeta: metav1.ObjectMeta{Name: "dra-off"},
			Spec: ghostgpuv1alpha1.GPUPoolSpec{
				ModelRef:    modelName,
				GPUsPerNode: 2,
				Advertise:   ghostgpuv1alpha1.AdvertiseSpec{DRA: ptr.To(false)},
			},
		}
		Expect(k8sClient.Create(ctx, off)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, off))).To(Succeed())
		})

		fetched := &ghostgpuv1alpha1.GPUPool{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "dra-off"}, fetched)).To(Succeed())

		Expect(fetched.Spec.Advertise.DRAEnabled()).To(BeFalse(), "explicit dra=false was defaulted back to true")
		Expect(fetched.Spec.Advertise.ExtendedResourceEnabled()).To(BeTrue(), "the other path should still default")
	})

	It("defaults the model vendor to nvidia", func() {
		model := &ghostgpuv1alpha1.GPUModel{}
		Expect(k8sClient.Get(ctx, modelKey, model)).To(Succeed())
		Expect(model.Spec.Vendor).To(Equal("nvidia"))
	})

	It("rejects a pool exceeding the DRA per-slice device limit", func() {
		tooMany := &ghostgpuv1alpha1.GPUPool{
			ObjectMeta: metav1.ObjectMeta{Name: "too-many-gpus"},
			Spec: ghostgpuv1alpha1.GPUPoolSpec{
				ModelRef:    modelName,
				GPUsPerNode: 129,
			},
		}
		Expect(k8sClient.Create(ctx, tooMany)).NotTo(Succeed())
	})

	It("rejects a model with a malformed compute capability", func() {
		bad := &ghostgpuv1alpha1.GPUModel{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-compute"},
			Spec: ghostgpuv1alpha1.GPUModelSpec{
				ProductName:       "X",
				Memory:            resource.MustParse("1Gi"),
				ComputeCapability: "nine",
			},
		}
		Expect(k8sClient.Create(ctx, bad)).NotTo(Succeed())
	})

	It("reconciles without error", func() {
		r := &GPUPoolReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: poolKey})
		Expect(err).NotTo(HaveOccurred())
	})
})
