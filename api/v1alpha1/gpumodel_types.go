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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GPUModelSpec describes a GPU hardware archetype to simulate.
//
// A GPUModel carries only properties intrinsic to the hardware itself.
// How many of them exist, and where, is described by a GPUPool.
type GPUModelSpec struct {
	// vendor of the simulated GPU.
	// +kubebuilder:validation:Enum=nvidia
	// +kubebuilder:default=nvidia
	// +optional
	Vendor string `json:"vendor,omitempty"`

	// productName as reported by GPU Feature Discovery, e.g. "NVIDIA-H100-SXM".
	// This is surfaced verbatim in the nvidia.com/gpu.product node label, so it
	// must match what real GFD would report for tooling to select on it.
	// +kubebuilder:validation:MinLength=1
	// +required
	ProductName string `json:"productName"`

	// memory available per GPU, e.g. "80Gi".
	// +required
	Memory resource.Quantity `json:"memory"`

	// computeCapability of the GPU, e.g. "9.0".
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+$`
	// +required
	ComputeCapability string `json:"computeCapability"`
}

// GPUModelStatus defines the observed state of GPUModel.
type GPUModelStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the GPUModel resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Product",type=string,JSONPath=`.spec.productName`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.memory`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GPUModel is the Schema for the gpumodels API
type GPUModel struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GPUModel
	// +required
	Spec GPUModelSpec `json:"spec"`

	// status defines the observed state of GPUModel
	// +optional
	Status GPUModelStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GPUModelList contains a list of GPUModel
type GPUModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GPUModel `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &GPUModel{}, &GPUModelList{})
		return nil
	})
}
