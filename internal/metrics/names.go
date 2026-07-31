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

// Package metrics publishes DCGM-shaped Prometheus metrics for simulated GPUs.
//
// The point is not that ghostgpu has a metrics endpoint — that is table stakes,
// and other simulators have one. It is that the numbers are *attributable* and
// *deterministic*.
//
// Attribution comes free from the DRA-first design: the scheduler records exact
// device identity in ResourceClaim.status, so the namespace/pod/container labels
// are correct by construction, including per MIG instance. Real dcgm-exporter
// deployments have a long tail of bugs in exactly that area, because they have
// to re-derive the binding from the container runtime.
//
// Determinism is the other half. A metric that jitters randomly cannot be
// asserted against, and these numbers exist to drive a rule under test.
// Everything here is a pure function of declared spec plus observed allocation.
package metrics

// DCGM metric names.
//
// These are an external contract in the strongest sense: dashboards, recording
// rules, and KEDA queries hardcode them, so a typo makes ghostgpu invisible to
// the tooling it exists to test. Taken from dcgm-exporter's default counter set
// rather than from memory, and pinned by TestMetricNamesMatchDCGM.
const (
	// Utilization.
	GPUUtil     = "DCGM_FI_DEV_GPU_UTIL"
	MemCopyUtil = "DCGM_FI_DEV_MEM_COPY_UTIL"

	// Framebuffer, in MiB.
	FBUsed = "DCGM_FI_DEV_FB_USED"
	FBFree = "DCGM_FI_DEV_FB_FREE"

	// Profiling ratios, 0 to 1.
	GREngineActive = "DCGM_FI_PROF_GR_ENGINE_ACTIVE"
	TensorActive   = "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE"

	// Physical readings. Properties of the card, never of a MIG instance.
	PowerUsage = "DCGM_FI_DEV_POWER_USAGE"
	GPUTemp    = "DCGM_FI_DEV_GPU_TEMP"

	// XIDErrors is the last XID error the driver reported. Zero means healthy.
	// Present from the start because fault injection is largely "make this
	// report 79 and see whether the operator drains the node".
	XIDErrors = "DCGM_FI_DEV_XID_ERRORS"
)

// DCGM metric label names, likewise taken from the real exporter.
//
// Hostname carries the *simulated* node's name. Real dcgm-exporter runs as a
// DaemonSet and reports its own host, which ghostgpu cannot do: kwok nodes have
// no kubelet, so nothing can run on them. Publishing the whole fleet from one
// endpoint with Hostname per simulated node keeps every query that groups or
// filters by node working unchanged, which is the property that matters.
const (
	LabelGPU       = "gpu"
	LabelUUID      = "UUID"
	LabelDevice    = "device"
	LabelModelName = "modelName"
	LabelHostname  = "Hostname"

	LabelNamespace = "namespace"
	LabelPod       = "pod"
	LabelContainer = "container"

	LabelMIGInstanceID = "GPU_I_ID"
	LabelMIGProfile    = "GPU_I_PROFILE"
)

// Port is the port dcgm-exporter conventionally serves on. Reusing it means an
// existing scrape config finds ghostgpu without being rewritten.
const Port = 9400

// Path is the conventional Prometheus endpoint path.
const Path = "/metrics"
