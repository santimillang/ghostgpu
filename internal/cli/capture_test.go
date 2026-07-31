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

package cli

import (
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// Fixtures shared across the package's tests.
const (
	h100Product = "NVIDIA-H100-80GB-HBM3"
	h100Model   = "h100-80gb-hbm3"
	h100Memory  = "80Gi"
	h100Compute = "9.0"
	a100Product = "NVIDIA-A100-SXM4-40GB"

	// sourceNode is the node name the capture fixtures read from. Captured
	// output never contains it, which is the point of TestCaptureAnonymisesNodeNames.
	sourceNode = "n1"
)

// gfdNode builds a node labelled the way GPU Feature Discovery would label it.
func gfdNode(name string, labels map[string]string, capacity corev1.ResourceList) corev1.Node {
	if capacity == nil {
		capacity = corev1.ResourceList{}
	}
	if _, ok := capacity[corev1.ResourceCPU]; !ok {
		capacity[corev1.ResourceCPU] = resource.MustParse("96")
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Capacity:    capacity,
			Allocatable: capacity,
		},
	}
}

// wholeGPULabels is what GFD reports for an unpartitioned node.
func wholeGPULabels(product string, count int) map[string]string {
	return map[string]string{
		gpu.LabelGPUPresent:   labelTrue,
		gpu.LabelGPUProduct:   product,
		gpu.LabelGPUMemory:    "81920", // 80Gi in MiB, as GFD reports it
		gpu.LabelComputeMajor: "9",
		gpu.LabelComputeMinor: "0",
		gpu.LabelGPUCount:     strconv.Itoa(count),
	}
}

func modelsIn(objs []client.Object) []*v1alpha1.GPUModel {
	var out []*v1alpha1.GPUModel
	for _, o := range objs {
		if m, ok := o.(*v1alpha1.GPUModel); ok {
			out = append(out, m)
		}
	}
	return out
}

func poolsIn(objs []client.Object) []*v1alpha1.GPUPool {
	var out []*v1alpha1.GPUPool
	for _, o := range objs {
		if p, ok := o.(*v1alpha1.GPUPool); ok {
			out = append(out, p)
		}
	}
	return out
}

func nodesIn(objs []client.Object) []*corev1.Node {
	var out []*corev1.Node
	for _, o := range objs {
		if n, ok := o.(*corev1.Node); ok {
			out = append(out, n)
		}
	}
	return out
}

func mustCapture(t *testing.T, nodes []corev1.Node, slices []resourcev1.ResourceSlice, opts CaptureOptions) CaptureResult {
	t.Helper()
	got, err := Capture(nodes, slices, opts)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return got
}

func TestCaptureWholeGPUNodes(t *testing.T) {
	nodes := []corev1.Node{
		gfdNode("ip-10-0-1-7.ec2.internal", wholeGPULabels(h100Product, 8), nil),
		gfdNode("ip-10-0-1-8.ec2.internal", wholeGPULabels(h100Product, 8), nil),
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	models := modelsIn(got.Objects)
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	m := models[0]
	if m.Name != h100Model {
		t.Errorf("model name = %q, want %q", m.Name, h100Model)
	}
	if m.Spec.ProductName != h100Product {
		t.Errorf("productName = %q, want %q", m.Spec.ProductName, h100Product)
	}
	if m.Spec.Memory.String() != h100Memory {
		t.Errorf("memory = %s, want %s", m.Spec.Memory.String(), h100Memory)
	}
	if m.Spec.ComputeCapability != h100Compute {
		t.Errorf("computeCapability = %q, want %s", m.Spec.ComputeCapability, h100Compute)
	}
	if m.Kind != kindGPUModel || m.APIVersion == "" {
		t.Errorf("TypeMeta = %+v, want it set so the YAML is applyable", m.TypeMeta)
	}

	pools := poolsIn(got.Objects)
	if len(pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(pools))
	}
	p := pools[0]
	if p.Name != h100Model+"-pool" {
		t.Errorf("pool name = %q, want %q", p.Name, h100Model+"-pool")
	}
	if p.Spec.ModelRef != m.Name {
		t.Errorf("modelRef = %q, want %q", p.Spec.ModelRef, m.Name)
	}
	if p.Spec.GPUsPerNode != 8 {
		t.Errorf("gpusPerNode = %d, want 8", p.Spec.GPUsPerNode)
	}
	if p.Spec.SharingMode != v1alpha1.SharingModeNone {
		t.Errorf("sharingMode = %q, want none", p.Spec.SharingMode)
	}
	if p.Spec.NodeSelector[ShapeLabel] != p.Name {
		t.Errorf("nodeSelector = %v, want %s=%s", p.Spec.NodeSelector, ShapeLabel, p.Name)
	}

	simulated := nodesIn(got.Objects)
	if len(simulated) != 2 {
		t.Fatalf("nodes = %d, want 2", len(simulated))
	}
	for _, n := range simulated {
		if n.Annotations[kwokNodeAnnotation] != kwokNodeValue {
			t.Errorf("node %s is missing the kwok annotation, so ghostgpu would refuse to touch it", n.Name)
		}
		if n.Labels[ShapeLabel] != p.Name {
			t.Errorf("node %s shape label = %q, want %q", n.Name, n.Labels[ShapeLabel], p.Name)
		}
		if n.Status.Capacity.Cpu().String() != "96" {
			t.Errorf("node %s cpu = %s, want the source node's 96", n.Name, n.Status.Capacity.Cpu())
		}
	}
}

// TestCaptureAnonymisesNodeNames pins a privacy property, not a cosmetic one.
// Real node names carry internal hostnames and topology, and captured output is
// exactly the kind of thing that gets pasted into a public issue.
func TestCaptureAnonymisesNodeNames(t *testing.T) {
	const secret = "ip-10-0-1-7.prod.internal.example.com"
	nodes := []corev1.Node{gfdNode(secret, wholeGPULabels(h100Product, 8), nil)}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	rendered, err := RenderYAML(got.Objects)
	if err != nil {
		t.Fatalf("RenderYAML: %v", err)
	}
	if strings.Contains(rendered, secret) {
		t.Errorf("captured output leaks the source node name %q:\n%s", secret, rendered)
	}
	if strings.Contains(rendered, "prod.internal") {
		t.Errorf("captured output leaks source topology:\n%s", rendered)
	}
}

func TestCaptureSeparatesDistinctShapes(t *testing.T) {
	a100Labels := wholeGPULabels(a100Product, 8)
	a100Labels[gpu.LabelGPUMemory] = "40960"
	a100Labels[gpu.LabelComputeMajor] = "8"

	nodes := []corev1.Node{
		gfdNode(sourceNode, wholeGPULabels(h100Product, 8), nil),
		gfdNode("n2", a100Labels, nil),
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	if n := len(modelsIn(got.Objects)); n != 2 {
		t.Errorf("models = %d, want 2", n)
	}
	pools := poolsIn(got.Objects)
	if len(pools) != 2 {
		t.Fatalf("pools = %d, want 2", len(pools))
	}
	seen := map[string]bool{}
	for _, p := range pools {
		if seen[p.Name] {
			t.Errorf("duplicate pool name %q", p.Name)
		}
		seen[p.Name] = true
	}
}

// One product, two node shapes: the hardware is the same, so it is one GPUModel,
// but the pools must stay distinct and distinguishably named.
func TestCaptureSharesOneModelAcrossShapes(t *testing.T) {
	nodes := []corev1.Node{
		gfdNode(sourceNode, wholeGPULabels(h100Product, 8), nil),
		gfdNode("n2", wholeGPULabels(h100Product, 4), nil),
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	models := modelsIn(got.Objects)
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1 — same hardware", len(models))
	}
	pools := poolsIn(got.Objects)
	if len(pools) != 2 {
		t.Fatalf("pools = %d, want 2", len(pools))
	}
	names := []string{pools[0].Name, pools[1].Name}
	for _, want := range []string{h100Model + "-4gpu", h100Model + "-8gpu"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("pool %q missing from %v", want, names)
		}
	}
	for _, p := range pools {
		if p.Spec.ModelRef != models[0].Name {
			t.Errorf("pool %s modelRef = %q, want %q", p.Name, p.Spec.ModelRef, models[0].Name)
		}
	}
}

// migLabels is what GFD reports under the mixed strategy: MIG is on, and
// gpu.count reports the whole cards left unpartitioned, which is none.
func migLabels(product string) map[string]string {
	labels := wholeGPULabels(product, 0)
	labels[gpu.LabelMIGCapable] = labelTrue
	labels[gpu.LabelMIGStrategy] = gpu.MIGStrategyMixed
	return labels
}

// budgetSlice stands in for the ResourceSlice a DRA driver publishes: one
// shared counter set per physical GPU.
func budgetSlice(gpus int32) resourcev1.ResourceSlice {
	sets := make([]resourcev1.CounterSet, 0, gpus)
	for i := range gpus {
		sets = append(sets, resourcev1.CounterSet{Name: mig.CounterSetName(i)})
	}
	name := sourceNode
	return resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: sourceNode + "-counters"},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:         gpu.DriverName,
			NodeName:       &name,
			Pool:           resourcev1.ResourcePool{Name: sourceNode},
			SharedCounters: sets,
		},
	}
}

func TestCaptureMIGPartitionFromExtendedResources(t *testing.T) {
	capacity := corev1.ResourceList{
		gpu.MIGResourceName("1g.10gb"): resource.MustParse("32"),
		gpu.MIGResourceName("3g.40gb"): resource.MustParse("8"),
		gpu.GPUResourceName:            resource.MustParse("0"),
	}
	nodes := []corev1.Node{gfdNode(sourceNode, migLabels(h100Product), capacity)}
	slices := []resourcev1.ResourceSlice{budgetSlice(8)}

	got := mustCapture(t, nodes, slices, CaptureOptions{EmitNodes: true})

	pools := poolsIn(got.Objects)
	if len(pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(pools))
	}
	p := pools[0]
	if p.Spec.SharingMode != v1alpha1.SharingModeMIG {
		t.Errorf("sharingMode = %q, want mig", p.Spec.SharingMode)
	}
	if p.Spec.GPUsPerNode != 8 {
		t.Errorf("gpusPerNode = %d, want 8 from the counter sets", p.Spec.GPUsPerNode)
	}

	want := map[string]int32{"1g.10gb": 4, "3g.40gb": 1}
	if len(p.Spec.MIGPartition) != len(want) {
		t.Fatalf("partition = %+v, want %v", p.Spec.MIGPartition, want)
	}
	for _, e := range p.Spec.MIGPartition {
		if want[e.Profile] != e.Count {
			t.Errorf("partition %s = %d, want %d", e.Profile, e.Count, want[e.Profile])
		}
	}
	// A partition entry order that depends on map iteration would make the
	// output undiffable between runs.
	if p.Spec.MIGPartition[0].Profile != "1g.10gb" {
		t.Errorf("partition is not sorted by profile: %+v", p.Spec.MIGPartition)
	}
}

// A known product needs no migProfiles: leaving them out lets the operator
// resolve its own table rather than freezing today's numbers into the manifest.
func TestCaptureOmitsProfilesForKnownHardware(t *testing.T) {
	capacity := corev1.ResourceList{gpu.MIGResourceName("1g.10gb"): resource.MustParse("56")}
	nodes := []corev1.Node{gfdNode(sourceNode, migLabels(h100Product), capacity)}
	slices := []resourcev1.ResourceSlice{budgetSlice(8)}

	got := mustCapture(t, nodes, slices, CaptureOptions{})

	models := modelsIn(got.Objects)
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	if len(models[0].Spec.MIGProfiles) != 0 {
		t.Errorf("migProfiles = %+v, want empty for hardware ghostgpu knows", models[0].Spec.MIGProfiles)
	}
}

// Hardware ghostgpu has no table for is the case capture has to handle, since
// the whole point is reproducing a cluster ghostgpu was not told about.
func TestCaptureDerivesProfilesForUnknownHardware(t *testing.T) {
	capacity := corev1.ResourceList{
		gpu.MIGResourceName("2g.24gb"): resource.MustParse("4"),
		gpu.MIGResourceName("1g.12gb"): resource.MustParse("2"),
	}
	nodes := []corev1.Node{gfdNode(sourceNode, migLabels("ACME-Accelerator-X1"), capacity)}
	slices := []resourcev1.ResourceSlice{budgetSlice(2)}

	got := mustCapture(t, nodes, slices, CaptureOptions{})

	models := modelsIn(got.Objects)
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	profiles := models[0].Spec.MIGProfiles
	if len(profiles) != 2 {
		t.Fatalf("migProfiles = %+v, want 2", profiles)
	}
	if profiles[0].Name != "1g.12gb" || profiles[0].Slices != 1 || profiles[0].Memory.String() != "12Gi" {
		t.Errorf("profiles[0] = %+v, want 1g.12gb/1 slice/12Gi", profiles[0])
	}
	if profiles[1].Name != "2g.24gb" || profiles[1].Slices != 2 {
		t.Errorf("profiles[1] = %+v, want 2g.24gb/2 slices", profiles[1])
	}
}

func TestCaptureWarnsOnNonUniformPartition(t *testing.T) {
	// Five instances across two GPUs cannot be a uniform per-GPU layout, and
	// migPartition can only express a uniform one.
	capacity := corev1.ResourceList{gpu.MIGResourceName("1g.10gb"): resource.MustParse("5")}
	nodes := []corev1.Node{gfdNode(sourceNode, migLabels(h100Product), capacity)}
	slices := []resourcev1.ResourceSlice{budgetSlice(2)}

	got := mustCapture(t, nodes, slices, CaptureOptions{})

	if len(got.Warnings) == 0 {
		t.Fatal("want a warning that the layout was rounded, got none")
	}
	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, "1g.10gb") {
		t.Errorf("warning does not name the profile: %s", joined)
	}
	pools := poolsIn(got.Objects)
	if len(pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(pools))
	}
	if len(pools[0].Spec.MIGPartition) != 1 || pools[0].Spec.MIGPartition[0].Count != 2 {
		t.Errorf("partition = %+v, want 1g.10gb rounded down to 2 per GPU", pools[0].Spec.MIGPartition)
	}
}

// Under the mixed strategy gpu.count is zero by design, so without a DRA driver
// publishing per-GPU counters there is nothing in the cluster that says how
// many physical cards there are. Guessing would silently simulate the wrong
// hardware, so capture says so instead.
func TestCaptureRefusesToGuessPhysicalGPUsUnderMIG(t *testing.T) {
	capacity := corev1.ResourceList{gpu.MIGResourceName("1g.10gb"): resource.MustParse("56")}
	nodes := []corev1.Node{gfdNode(sourceNode, migLabels(h100Product), capacity)}

	_, err := Capture(nodes, nil, CaptureOptions{})
	if err == nil {
		t.Fatal("want an error when the physical GPU count cannot be determined")
	}
	if !strings.Contains(err.Error(), "--gpus-per-node") {
		t.Errorf("error should point at the flag that fixes it, got: %v", err)
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{GPUsPerNode: 8})
	pools := poolsIn(got.Objects)
	if len(pools) != 1 || pools[0].Spec.GPUsPerNode != 8 {
		t.Fatalf("with the override, gpusPerNode = %+v, want 8", pools)
	}
	if pools[0].Spec.MIGPartition[0].Count != 7 {
		t.Errorf("partition = %+v, want 7 per GPU", pools[0].Spec.MIGPartition)
	}
}

func TestCaptureSkipsNodesWithoutGPUs(t *testing.T) {
	nodes := []corev1.Node{
		gfdNode("control-plane", map[string]string{"kubernetes.io/role": "master"}, nil),
		gfdNode("gpu-node", wholeGPULabels(h100Product, 8), nil),
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	if n := len(poolsIn(got.Objects)); n != 1 {
		t.Errorf("pools = %d, want 1", n)
	}
	if n := len(nodesIn(got.Objects)); n != 1 {
		t.Errorf("nodes = %d, want only the GPU node reproduced", n)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("a node with no GPUs is unremarkable and should not warn: %v", got.Warnings)
	}
}

func TestCaptureWarnsOnIncompleteLabels(t *testing.T) {
	labels := wholeGPULabels(h100Product, 8)
	delete(labels, gpu.LabelComputeMajor)

	nodes := []corev1.Node{
		gfdNode("half-labelled", labels, nil),
		gfdNode("good", wholeGPULabels(h100Product, 8), nil),
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	if len(got.Warnings) == 0 {
		t.Fatal("want a warning naming the skipped node")
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "half-labelled") {
		t.Errorf("warning should name the node it skipped: %v", got.Warnings)
	}
	if n := len(nodesIn(got.Objects)); n != 1 {
		t.Errorf("nodes = %d, want 1 — the incompletely labelled node is not reproduced", n)
	}
}

func TestCaptureNoGPUNodesIsAnError(t *testing.T) {
	nodes := []corev1.Node{gfdNode("plain", nil, nil)}

	if _, err := Capture(nodes, nil, CaptureOptions{}); err == nil {
		t.Fatal("want an error when the cluster has no GPU nodes at all")
	}
}

// topologySlice is what ghostgpu itself publishes for a whole-GPU pool, which
// is where the interconnect shape lives — no node label carries it.
func topologySlice(node string, gpus int32, domainSize int32, numa bool) resourcev1.ResourceSlice {
	devices := make([]resourcev1.Device, 0, gpus)
	for i := range gpus {
		attrs := map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
			gpu.AttrUUID: {StringValue: ptr.To(gpu.DeviceUUID(node, i))},
		}
		if d := gpu.NVLinkDomain(i, domainSize); d != "" {
			attrs[gpu.AttrNVLinkDomain] = resourcev1.DeviceAttribute{StringValue: ptr.To(d)}
		}
		if numa {
			attrs[gpu.AttrNUMANode] = resourcev1.DeviceAttribute{IntValue: ptr.To(gpu.NUMANode(i, domainSize))}
		}
		devices = append(devices, resourcev1.Device{Name: gpu.DeviceName(node, i), Attributes: attrs})
	}
	name := node
	return resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: node + "-devices"},
		Spec: resourcev1.ResourceSliceSpec{
			Driver:   gpu.DriverName,
			NodeName: &name,
			Pool:     resourcev1.ResourcePool{Name: node},
			Devices:  devices,
		},
	}
}

func TestCaptureTopologyFromResourceSlices(t *testing.T) {
	nodes := []corev1.Node{gfdNode(sourceNode, wholeGPULabels(h100Product, 8), nil)}
	slices := []resourcev1.ResourceSlice{topologySlice(sourceNode, 8, 4, true)}

	got := mustCapture(t, nodes, slices, CaptureOptions{})

	pools := poolsIn(got.Objects)
	if len(pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(pools))
	}
	if pools[0].Spec.Topology.NVLinkDomainSize != 4 {
		t.Errorf("nvlinkDomainSize = %d, want 4", pools[0].Spec.Topology.NVLinkDomainSize)
	}
	if !pools[0].Spec.Topology.NUMAAware {
		t.Error("numaAware = false, want true")
	}
}

// A cluster with no DRA at all still captures; it simply has no topology to
// report, and reporting none is better than inventing one.
func TestCaptureWithoutResourceSlicesHasNoTopology(t *testing.T) {
	nodes := []corev1.Node{gfdNode(sourceNode, wholeGPULabels(h100Product, 8), nil)}

	got := mustCapture(t, nodes, nil, CaptureOptions{})

	pools := poolsIn(got.Objects)
	if pools[0].Spec.Topology.NVLinkDomainSize != 0 || pools[0].Spec.Topology.NUMAAware {
		t.Errorf("topology = %+v, want empty", pools[0].Spec.Topology)
	}
}

func TestCaptureWithoutNodesStillEmitsPools(t *testing.T) {
	nodes := []corev1.Node{gfdNode(sourceNode, wholeGPULabels(h100Product, 8), nil)}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: false})

	if n := len(nodesIn(got.Objects)); n != 0 {
		t.Errorf("nodes = %d, want 0", n)
	}
	if n := len(poolsIn(got.Objects)); n != 1 {
		t.Errorf("pools = %d, want 1", n)
	}
	// The pool still selects on a label nothing carries, so a user who skipped
	// the nodes needs to be told what to label.
	if len(got.Warnings) == 0 || !strings.Contains(strings.Join(got.Warnings, "\n"), ShapeLabel) {
		t.Errorf("want a warning naming %s, got %v", ShapeLabel, got.Warnings)
	}
}

// Captured output gets committed, diffed, and pasted into issues, so identical
// input must render identically — map iteration order would ruin that.
func TestCaptureIsDeterministic(t *testing.T) {
	build := func() string {
		nodes := []corev1.Node{
			gfdNode(sourceNode, wholeGPULabels(h100Product, 8), nil),
			gfdNode("n2", wholeGPULabels(a100Product, 4), nil),
			gfdNode("n3", wholeGPULabels(h100Product, 8), nil),
		}
		got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})
		out, err := RenderYAML(got.Objects)
		if err != nil {
			t.Fatalf("RenderYAML: %v", err)
		}
		return out
	}

	if first, second := build(), build(); first != second {
		t.Errorf("capture is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestCaptureRoundTrip is the acceptance criterion that matters most: point
// capture at a cluster ghostgpu itself simulated, and get back the pool it
// started from. It builds the node exactly as the operator would — same label
// function, same resource function, same slice builder — so a change to either
// side that breaks the inverse shows up here.
func TestCaptureRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts UpOptions
	}{
		{
			name: "whole GPUs with topology",
			opts: UpOptions{
				Name: "src", Product: h100Product, Memory: h100Memory, Compute: h100Compute,
				GPUsPerNode: 8, NVLinkDomainSize: 4, NUMAAware: true,
				DRA: true, ExtendedResource: true,
			},
		},
		{
			name: "static MIG",
			opts: UpOptions{
				Name: "src", Product: h100Product, Memory: h100Memory, Compute: h100Compute,
				GPUsPerNode: 4, SharingMode: string(v1alpha1.SharingModeMIG),
				MIGPartition: "3g.40gb=1,1g.10gb=4",
				DRA:          true, ExtendedResource: true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs, err := BuildManifests(tc.opts)
			if err != nil {
				t.Fatalf("BuildManifests: %v", err)
			}
			model := objs[0].(*v1alpha1.GPUModel)
			pool := objs[1].(*v1alpha1.GPUPool)

			table, err := mig.Resolve(model)
			if err != nil {
				t.Fatalf("mig.Resolve: %v", err)
			}

			// The node as the operator would leave it.
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "sim-0", Labels: gpu.NodeLabels(pool, model)},
				Status:     corev1.NodeStatus{Capacity: gpu.NodeResources(pool, table)},
			}
			node.Status.Allocatable = node.Status.Capacity

			var published []resourcev1.ResourceSlice
			if pool.Spec.MIGEnabled() {
				for _, s := range gpu.BuildMIGSlices(pool, model, table, node.Name) {
					published = append(published, *s)
				}
			} else {
				published = append(published, *gpu.BuildResourceSlice(pool, model, node.Name))
			}

			got := mustCapture(t, []corev1.Node{node}, published, CaptureOptions{EmitNodes: true})

			captured := poolsIn(got.Objects)
			if len(captured) != 1 {
				t.Fatalf("pools = %d, want 1", len(captured))
			}
			c := captured[0]

			if c.Spec.GPUsPerNode != pool.Spec.GPUsPerNode {
				t.Errorf("gpusPerNode = %d, want %d", c.Spec.GPUsPerNode, pool.Spec.GPUsPerNode)
			}
			if c.Spec.SharingMode != pool.Spec.SharingMode {
				t.Errorf("sharingMode = %q, want %q", c.Spec.SharingMode, pool.Spec.SharingMode)
			}
			if c.Spec.Topology != pool.Spec.Topology {
				t.Errorf("topology = %+v, want %+v", c.Spec.Topology, pool.Spec.Topology)
			}
			if len(c.Spec.MIGPartition) != len(pool.Spec.MIGPartition) {
				t.Fatalf("partition = %+v, want %+v", c.Spec.MIGPartition, pool.Spec.MIGPartition)
			}
			want := map[string]int32{}
			for _, e := range pool.Spec.MIGPartition {
				want[e.Profile] = e.Count
			}
			for _, e := range c.Spec.MIGPartition {
				if want[e.Profile] != e.Count {
					t.Errorf("partition %s = %d, want %d", e.Profile, e.Count, want[e.Profile])
				}
			}

			models := modelsIn(got.Objects)
			if len(models) != 1 {
				t.Fatalf("models = %d, want 1", len(models))
			}
			if models[0].Spec.ProductName != model.Spec.ProductName {
				t.Errorf("productName = %q, want %q", models[0].Spec.ProductName, model.Spec.ProductName)
			}
			if models[0].Spec.Memory.Cmp(model.Spec.Memory) != 0 {
				t.Errorf("memory = %s, want %s", models[0].Spec.Memory.String(), model.Spec.Memory.String())
			}
			if models[0].Spec.ComputeCapability != model.Spec.ComputeCapability {
				t.Errorf("computeCapability = %q, want %q",
					models[0].Spec.ComputeCapability, model.Spec.ComputeCapability)
			}
			if got.Warnings != nil {
				t.Errorf("round trip should be clean, got warnings: %v", got.Warnings)
			}
		})
	}
}

// The single strategy advertises MIG instances as whole nvidia.com/gpu, which
// ghostgpu does not model. Capturing it as whole GPUs reproduces the scalar
// scheduling surface exactly, but not MIG exclusivity, and saying so is the
// difference between a lossy capture and a misleading one.
func TestCaptureWarnsOnSingleMIGStrategy(t *testing.T) {
	labels := wholeGPULabels(h100Product+"-MIG-1g.10gb", 56)
	labels[gpu.LabelMIGCapable] = labelTrue
	labels[gpu.LabelMIGStrategy] = "single"

	nodes := []corev1.Node{gfdNode(sourceNode, labels, nil)}

	got := mustCapture(t, nodes, nil, CaptureOptions{})

	if !strings.Contains(strings.Join(got.Warnings, "\n"), "single") {
		t.Errorf("want a warning about the single strategy, got %v", got.Warnings)
	}
	pools := poolsIn(got.Objects)
	if len(pools) != 1 || pools[0].Spec.SharingMode != v1alpha1.SharingModeNone {
		t.Errorf("sharingMode = %+v, want none", pools)
	}
	if pools[0].Spec.GPUsPerNode != 56 {
		t.Errorf("gpusPerNode = %d, want the 56 instances the cluster advertises", pools[0].Spec.GPUsPerNode)
	}
}

// A node can run MIG on some cards and leave others whole. A ghostgpu pool
// partitions every GPU it manages, so half of that node is not reproduced —
// which is exactly the kind of quiet infidelity a warning has to catch.
func TestCaptureWarnsOnMixedPartitionedAndWholeGPUs(t *testing.T) {
	labels := migLabels(h100Product)
	labels[gpu.LabelGPUCount] = "2" // two cards left unpartitioned

	capacity := corev1.ResourceList{gpu.MIGResourceName("1g.10gb"): resource.MustParse("42")}
	nodes := []corev1.Node{gfdNode(sourceNode, labels, capacity)}
	slices := []resourcev1.ResourceSlice{budgetSlice(6)}

	got := mustCapture(t, nodes, slices, CaptureOptions{})

	if !strings.Contains(strings.Join(got.Warnings, "\n"), "unpartitioned") {
		t.Errorf("want a warning about the whole cards, got %v", got.Warnings)
	}
}

// A whole-GPU DRA pool publishes one device per card, which is enough to size
// the node even with no gpu.count label at all.
func TestCapturePhysicalGPUsFromPublishedDevices(t *testing.T) {
	labels := wholeGPULabels(h100Product, 8)
	delete(labels, gpu.LabelGPUCount)

	nodes := []corev1.Node{gfdNode(sourceNode, labels, nil)}
	slices := []resourcev1.ResourceSlice{topologySlice(sourceNode, 8, 0, false)}

	got := mustCapture(t, nodes, slices, CaptureOptions{})

	pools := poolsIn(got.Objects)
	if len(pools) != 1 {
		t.Fatalf("pools = %d, want 1", len(pools))
	}
	if pools[0].Spec.GPUsPerNode != 8 {
		t.Errorf("gpusPerNode = %d, want 8 from the published devices", pools[0].Spec.GPUsPerNode)
	}
}

func TestCaptureRefusesToGuessWholeGPUCount(t *testing.T) {
	labels := wholeGPULabels(h100Product, 8)
	delete(labels, gpu.LabelGPUCount)

	_, err := Capture([]corev1.Node{gfdNode(sourceNode, labels, nil)}, nil, CaptureOptions{})
	if err == nil {
		t.Fatal("want an error when nothing says how many GPUs the node has")
	}
	if !strings.Contains(err.Error(), "--gpus-per-node") {
		t.Errorf("error should point at the flag that fixes it, got: %v", err)
	}
}

// Two products that reduce to the same object name must not produce two
// manifests fighting over it — the second silently overwrites the first on
// apply, and the fleet comes out wrong.
func TestCaptureNamesDoNotCollide(t *testing.T) {
	nodes := []corev1.Node{
		gfdNode(sourceNode, wholeGPULabels("NVIDIA H100 80GB HBM3", 8), nil),
		gfdNode("n2", wholeGPULabels("nvidia_h100_80gb_hbm3", 8), nil),
	}

	got := mustCapture(t, nodes, nil, CaptureOptions{EmitNodes: true})

	seen := map[string]bool{}
	for _, o := range got.Objects {
		key := o.GetObjectKind().GroupVersionKind().Kind + "/" + o.GetName()
		if seen[key] {
			t.Errorf("duplicate object %s", key)
		}
		seen[key] = true
	}
	if n := len(modelsIn(got.Objects)); n != 2 {
		t.Errorf("models = %d, want 2 — the product strings differ verbatim", n)
	}
}

// mig.capable says the hardware could do MIG, not that it is doing it. Treating
// it as "MIG is on" would partition every idle A100 in the fleet.
func TestCaptureIgnoresMIGCapableWithoutMIG(t *testing.T) {
	labels := wholeGPULabels(a100Product, 8)
	labels[gpu.LabelMIGCapable] = labelTrue

	nodes := []corev1.Node{gfdNode(sourceNode, labels, nil)}

	got := mustCapture(t, nodes, nil, CaptureOptions{})

	pools := poolsIn(got.Objects)
	if len(pools) != 1 || pools[0].Spec.SharingMode != v1alpha1.SharingModeNone {
		t.Errorf("sharingMode = %+v, want none", pools)
	}
}
