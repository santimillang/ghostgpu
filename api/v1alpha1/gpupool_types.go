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
	DRA *bool `json:"dra,omitempty"`

	// extendedResource patches node status with nvidia.com/gpu for consumers
	// that predate DRA. Compatibility only: a scalar resource cannot express
	// which GPU was assigned to which pod.
	// +kubebuilder:default=true
	// +optional
	ExtendedResource *bool `json:"extendedResource,omitempty"`
}

// These fields are pointers because they default to true. A plain bool with
// omitempty serializes an explicit false as absent, which the API server then
// defaults straight back to true — making the field impossible to turn off
// from any Go client. A nil pointer means "unset", which the accessors below
// resolve to the same default the CRD schema declares.

// DRAEnabled reports whether DRA ResourceSlices should be published.
func (a AdvertiseSpec) DRAEnabled() bool {
	return a.DRA == nil || *a.DRA
}

// ExtendedResourceEnabled reports whether node capacity should be patched.
func (a AdvertiseSpec) ExtendedResourceEnabled() bool {
	return a.ExtendedResource == nil || *a.ExtendedResource
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

	// advertise selects which resource models this pool publishes.
	//
	// Defaulted to an empty object so that a manifest omitting the key still
	// receives the nested field defaults. Without it, the API server has no
	// object to default into and both paths silently resolve to false, leaving
	// the pool advertising nothing at all.
	// +kubebuilder:default={}
	// +optional
	Advertise AdvertiseSpec `json:"advertise,omitempty"`

	// sharingMode is how each physical GPU is divided.
	//
	// "none" advertises whole GPUs. "mig" partitions each GPU into MIG
	// instances drawn from a shared compute-slice and memory budget, so
	// overlapping profiles on one GPU are mutually exclusive.
	//
	// Unlike advertise, this is safe as a plain string: its zero value and its
	// default are the same, so an omitted key and an explicit "none" are
	// indistinguishable by design rather than by accident.
	// +kubebuilder:validation:Enum=none;mig
	// +kubebuilder:default=none
	// +optional
	SharingMode SharingMode `json:"sharingMode,omitempty"`

	// +optional
	Topology TopologySpec `json:"topology,omitempty"`
}

// SharingMode is how a physical GPU is divided among workloads.
type SharingMode string

const (
	// SharingModeNone advertises whole, undivided GPUs.
	SharingModeNone SharingMode = "none"

	// SharingModeMIG partitions each GPU into MIG instances.
	SharingModeMIG SharingMode = "mig"

	// Time-slicing is planned for v0.3 and is deliberately absent from the
	// enum. Accepting it now would let users write manifests that silently
	// simulate nothing until the feature lands.
)

// MIGEnabled reports whether this pool partitions its GPUs into MIG instances.
func (s GPUPoolSpec) MIGEnabled() bool {
	return s.SharingMode == SharingModeMIG
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
