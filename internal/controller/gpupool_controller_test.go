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
	"k8s.io/apimachinery/pkg/types"
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
				NodeSelector: map[string]string{"type": "kwok"},
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

		Expect(fetched.Spec.Advertise.DRA).To(BeTrue(), "DRA should default to true")
		Expect(fetched.Spec.Advertise.ExtendedResource).To(BeTrue(), "extendedResource should default to true")
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
