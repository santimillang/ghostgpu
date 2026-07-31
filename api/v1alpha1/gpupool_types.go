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

	// migPartition declares which MIG instances actually exist on each GPU.
	//
	// Leave it empty to model *dynamic* MIG, as NVIDIA's DRA driver does: every
	// profile the hardware supports is advertised and the scheduler picks,
	// with the shared counters preventing overlapping choices.
	//
	// Set it to model *static* MIG, as the device plugin does: the instances
	// below are the only ones that exist, exactly as though an administrator
	// had created them with `nvidia-smi mig -cgi`. This is what makes the
	// legacy extended-resource path exact rather than approximate — the
	// per-profile counts then sum to something the hardware can actually
	// satisfy, because that is genuinely all there is.
	//
	// The declared instances must fit one GPU's budget.
	// +listType=map
	// +listMapKey=profile
	// +optional
	MIGPartition []MIGPartitionEntry `json:"migPartition,omitempty"`

	// faults declares GPUs that have failed.
	//
	// Hardware failure is one of the hardest things to test for, because it
	// cannot be arranged on demand: you cannot ask a production GPU to fall off
	// the bus to see whether your remediation drains the node. Declaring it
	// makes that scenario reproducible.
	//
	// Faults and occupancy are independent declarations, both applied
	// lowest-index-first, and a fault wins where they overlap — so "three busy,
	// one faulted" means the failure happened to a GPU that was working.
	// +optional
	Faults []FaultEntry `json:"faults,omitempty"`

	// utilization declares what a simulated GPU reports through the
	// DCGM-shaped metrics endpoint.
	//
	// Declared rather than randomised on purpose. A metric that jitters cannot
	// be asserted against, and the point of these numbers is to drive a rule
	// under test — "scale down when utilization stays below 20% for ten
	// minutes" is only testable if the fleet reliably does that.
	// +optional
	Utilization *UtilizationSpec `json:"utilization,omitempty"`

	// occupancy declares how much of the fleet is already busy before any
	// workload is submitted.
	//
	// Most interesting GPU scheduling bugs are about fragmentation rather than
	// capacity — "seven GPUs are free but spread 2/2/2/1 across four nodes;
	// does my four-GPU job schedule?" — and that situation cannot be built by
	// submitting filler pods, because where the filler lands is the scheduler's
	// choice and so the scenario under test is not reproducible.
	//
	// Entries are matched first-match-wins, so a list expresses a fleet that is
	// unevenly full. An entry with no selector matches every node, which makes
	// it a default when placed last.
	// +optional
	Occupancy []OccupancyEntry `json:"occupancy,omitempty"`

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

// MIGPartitionEntry is how many instances of one profile each GPU carries.
type MIGPartitionEntry struct {
	// profile names a MIG profile the GPU's model supports.
	// +kubebuilder:validation:Pattern=`^[0-9]+g\.[0-9]+gb$`
	// +required
	Profile string `json:"profile"`

	// count is how many instances of this profile exist per physical GPU.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=8
	// +required
	Count int32 `json:"count"`
}

// FaultEffect is what a failure does to work on the GPU.
type FaultEffect string

const (
	// FaultEffectEvict models a GPU that is gone: device loss, or an
	// uncorrectable ECC error. Anything running on it is thrown off and its
	// ResourceClaim released, so the workload can be rescheduled onto healthy
	// hardware — which is the behaviour a remediation or requeueing system
	// under test actually needs to exercise.
	FaultEffectEvict FaultEffect = "Evict"

	// FaultEffectUnschedulable models a GPU that still runs but must take no
	// new work, such as one with a row remap pending a reboot. Work already on
	// it is left alone.
	FaultEffectUnschedulable FaultEffect = "Unschedulable"
)

// FaultEntry declares failed GPUs on matching nodes.
type FaultEntry struct {
	// nodeSelector chooses which of the pool's nodes this entry applies to.
	// Empty matches every node in the pool.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// gpus is how many of each matching node's physical GPUs have failed,
	// counted from the lowest index so a scenario is reproducible.
	//
	// Under sharingMode "mig" this counts whole cards, as occupancy does:
	// every instance carved from a failed GPU fails with it, because they are
	// the same silicon.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=128
	// +required
	GPUs int32 `json:"gpus"`

	// effect is what the failure does to work on the GPU.
	// +kubebuilder:validation:Enum=Evict;Unschedulable
	// +kubebuilder:default=Evict
	// +optional
	Effect FaultEffect `json:"effect,omitempty"`

	// xid is the NVIDIA XID error code to report on DCGM_FI_DEV_XID_ERRORS for
	// the failed GPUs, which is the signal most remediation tooling watches.
	// 79 is "GPU has fallen off the bus"; 48 is a double-bit ECC error.
	//
	// Zero reports no error, which models a GPU that has been taken out of
	// service without the driver having logged anything.
	// +kubebuilder:validation:Minimum=0
	// +optional
	XID int32 `json:"xid,omitempty"`
}

// UtilizationSpec is what a simulated GPU reports in each of its two states.
type UtilizationSpec struct {
	// whenAllocated is reported by a GPU the scheduler has given to a pod, and
	// by one declared occupied. Defaults to fully busy.
	// +optional
	WhenAllocated *UtilizationSample `json:"whenAllocated,omitempty"`

	// whenIdle is reported by a GPU nothing holds. Defaults to zero, which is
	// what an idle GPU genuinely draws in every field ghostgpu models.
	// +optional
	WhenIdle *UtilizationSample `json:"whenIdle,omitempty"`

	// workloads override whenAllocated for GPUs held by matching pods.
	//
	// This is what makes a fleet heterogeneous, and heterogeneity is the whole
	// fixture that idle-GPU reclamation and utilization-based preemption need:
	// the question those tools answer is which of several jobs is wasting its
	// GPU, and a fleet where every allocated card reports the same number
	// cannot ask it.
	//
	// Entries are first-match-wins, so ordering is meaningful and an entry with
	// an empty selector reads as a default when placed last.
	// +optional
	Workloads []WorkloadUtilization `json:"workloads,omitempty"`
}

// WorkloadUtilization is what GPUs held by matching pods report.
type WorkloadUtilization struct {
	// podSelector chooses the pods this entry applies to. An empty selector
	// matches every pod.
	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// UtilizationSample is the reading those pods' GPUs report. Fields left
	// unset fall back to whenAllocated, and then to the busy defaults, so an
	// entry only has to state what makes this workload different.
	UtilizationSample `json:",inline"`
}

// UtilizationSample is one set of readings.
//
// Every field is a pointer so that an explicit zero is distinguishable from
// "unset", which matters because zero is a meaningful reading here rather than
// merely a default.
//
// Power and temperature are deliberately not defaulted. ghostgpu has no thermal
// or power model, and a plausible-looking wattage would be fabrication rather
// than simulation — so those series are simply absent until declared. The
// utilization and framebuffer fields do default, because zero for an idle GPU
// is a fact rather than a guess.
type UtilizationSample struct {
	// gpuUtil is the percentage reported as DCGM_FI_DEV_GPU_UTIL.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	GPUUtil *int32 `json:"gpuUtil,omitempty"`

	// memCopyUtil is the percentage reported as DCGM_FI_DEV_MEM_COPY_UTIL.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	MemCopyUtil *int32 `json:"memCopyUtil,omitempty"`

	// fbUsedPercent is how much of the framebuffer is in use. Reported as
	// DCGM_FI_DEV_FB_USED and DCGM_FI_DEV_FB_FREE in MiB, derived from the
	// GPU's own memory so the two always sum to it.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	FBUsedPercent *int32 `json:"fbUsedPercent,omitempty"`

	// grEngineActivePercent becomes the 0-1 ratio DCGM_FI_PROF_GR_ENGINE_ACTIVE.
	// Expressed as a percentage because a percentage is far easier to write and
	// read in a manifest than a fraction.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	GREngineActivePercent *int32 `json:"grEngineActivePercent,omitempty"`

	// tensorActivePercent becomes the 0-1 ratio DCGM_FI_PROF_PIPE_TENSOR_ACTIVE.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	TensorActivePercent *int32 `json:"tensorActivePercent,omitempty"`

	// powerWatts is reported as DCGM_FI_DEV_POWER_USAGE. Omitted entirely when
	// unset rather than guessed.
	// +kubebuilder:validation:Minimum=0
	// +optional
	PowerWatts *int32 `json:"powerWatts,omitempty"`

	// temperatureC is reported as DCGM_FI_DEV_GPU_TEMP. Omitted entirely when
	// unset rather than guessed.
	// +optional
	TemperatureC *int32 `json:"temperatureC,omitempty"`
}

// OccupancyEntry declares how many GPUs are already busy on matching nodes.
type OccupancyEntry struct {
	// nodeSelector chooses which of the pool's nodes this entry applies to.
	// Empty matches every node in the pool.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// busyPerNode is how many of each matching node's physical GPUs are
	// already occupied. It must not exceed gpusPerNode.
	//
	// Under sharingMode "mig" this still counts whole cards: every instance
	// carved from an occupied GPU becomes unavailable. Anything else would be
	// wrong rather than merely simpler, because MIG instances draw on shared
	// counters — leaving one profile allocatable on an "occupied" card would
	// mean the card was not occupied at all.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=128
	// +required
	BusyPerNode int32 `json:"busyPerNode"`
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

	// devicesPublished is the total number of DRA devices advertised across all
	// matched nodes. Under sharingMode "none" that is one per simulated GPU;
	// under "mig" it is one per MIG instance, so it counts instances rather
	// than cards.
	// +optional
	DevicesPublished int32 `json:"devicesPublished,omitempty"`

	// gpusOccupied is how many physical GPUs across all matched nodes this
	// pool has declared busy, and so how much of the fleet was never available
	// in the first place.
	//
	// Reported separately from devicesPublished because the two answer
	// different questions: how much hardware exists, and how much of it a
	// workload could actually have.
	// +optional
	GPUsOccupied int32 `json:"gpusOccupied,omitempty"`

	// gpusFaulted is how many physical GPUs across all matched nodes this pool
	// has declared failed.
	// +optional
	GPUsFaulted int32 `json:"gpusFaulted,omitempty"`

	// migProfilesPublished is how many MIG profiles each simulated GPU is
	// partitioned into. Zero when the pool does not use MIG.
	//
	// Reported separately because devicesPublished alone cannot distinguish
	// "many cards" from "few cards, finely divided", and those behave very
	// differently under a scheduler.
	// +optional
	MIGProfilesPublished int32 `json:"migProfilesPublished,omitempty"`
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
