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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AdvertiseSpec selects how simulated GPUs are exposed to the cluster.
type AdvertiseSpec struct {
	// dra publishes DRA ResourceSlices. This is the primary path: the
	// scheduler records exact device identity in ResourceClaim.status, so no
	// separate pod-to-GPU binding store is needed.
	// +kubebuilder:default=true
	// +optional
	DRA bool `json:"dra,omitempty"`

	// extendedResource patches node status with nvidia.com/gpu for consumers
	// that predate DRA. Compatibility only: a scalar resource cannot express
	// which GPU was assigned to which pod.
	// +kubebuilder:default=true
	// +optional
	ExtendedResource bool `json:"extendedResource,omitempty"`
}

// TopologySpec describes simulated interconnect structure.
//
// Structure only. Bandwidth and latency are deliberately not simulated: no
// scheduler in the ecosystem consumes them today, and modeling them would add
// significant complexity for no testing value.
type TopologySpec struct {
	// nvlinkDomainSize is how many GPUs share an NVLink domain. 0 omits domain
	// attributes entirely.
	// +kubebuilder:validation:Minimum=0
	// +optional
	NVLinkDomainSize int32 `json:"nvlinkDomainSize,omitempty"`

	// numaAware emits a NUMA node attribute per simulated device.
	// +optional
	NUMAAware bool `json:"numaAware,omitempty"`
}

// GPUPoolSpec describes a set of simulated GPUs across matching nodes.
type GPUPoolSpec struct {
	// modelRef names the GPUModel describing this pool's hardware.
	// +kubebuilder:validation:MinLength=1
	// +required
	ModelRef string `json:"modelRef"`

	// nodeSelector chooses which nodes receive simulated GPUs. Only
	// kwok-managed nodes are ever modified, regardless of what this matches.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// gpusPerNode is how many simulated GPUs each matching node receives.
	// Capped at 128 to stay within the DRA per-ResourceSlice device limit.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=128
	// +required
	GPUsPerNode int32 `json:"gpusPerNode"`

	// +optional
	Advertise AdvertiseSpec `json:"advertise,omitempty"`

	// +optional
	Topology TopologySpec `json:"topology,omitempty"`
}

// GPUPoolStatus defines the observed state of GPUPool.
type GPUPoolStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the GPUPool resource.
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

	// observedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// nodesMatched is how many simulated nodes this pool currently manages.
	// +optional
	NodesMatched int32 `json:"nodesMatched,omitempty"`

	// devicesPublished is the total number of simulated GPUs advertised
	// across all matched nodes.
	// +optional
	DevicesPublished int32 `json:"devicesPublished,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.modelRef`
// +kubebuilder:printcolumn:name="Per Node",type=integer,JSONPath=`.spec.gpusPerNode`
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=`.status.nodesMatched`
// +kubebuilder:printcolumn:name="Devices",type=integer,JSONPath=`.status.devicesPublished`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GPUPool is the Schema for the gpupools API
type GPUPool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GPUPool
	// +required
	Spec GPUPoolSpec `json:"spec"`

	// status defines the observed state of GPUPool
	// +optional
	Status GPUPoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GPUPoolList contains a list of GPUPool
type GPUPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GPUPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &GPUPool{}, &GPUPoolList{})
		return nil
	})
}
