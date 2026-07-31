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
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
	"github.com/santimillang/ghostgpu/internal/mig"
)

// ShapeLabel is how a captured GPUPool finds the simulated nodes belonging to
// it.
//
// A captured fleet has one pool per distinct node shape, so the pools need
// selectors that tell those shapes apart. The GFD labels cannot serve: a
// freshly created kwok node does not carry them — ghostgpu is what applies
// them — so selecting on them would leave every pool matching nothing forever.
const ShapeLabel = "ghostgpu.dev/shape"

// Annotations and labels the emitted nodes carry so kwok adopts them.
const (
	kwokNodeAnnotation = "kwok.x-k8s.io/node"
	kwokNodeValue      = "fake"
	ttlAnnotation      = "node.alpha.kubernetes.io/ttl"
)

// labelTrue is the value a Kubernetes boolean label carries. Label values are
// strings, so this is the literal, not a formatted bool.
const labelTrue = "true"

// The node label kwok's own manifests use to mark simulated machines, which the
// emitted nodes carry so that a default `ghostgpu up` selector still finds them.
const (
	nodeTypeLabel = "type"
	nodeTypeKwok  = "kwok"
)

// maxInstancesPerGPU mirrors the migPartition CRD maximum.
const maxInstancesPerGPU = 8

// defaultPods is the pod capacity given to a simulated node whose source did
// not report one. A node with zero pod capacity schedules nothing at all, which
// would look like ghostgpu failing rather than like missing data.
const defaultPods = 110

// maxNameBase leaves room for the "-pool", "-<n>gpu-mig" and "-<index>"
// suffixes within the 63-character limit a node's hostname label must respect.
const maxNameBase = 50

// CaptureOptions tunes what is read out of a source cluster.
type CaptureOptions struct {
	// GPUsPerNode fills in the physical GPU count when nothing in the source
	// cluster reveals it. Zero derives it, and derivation always wins where it
	// succeeds: this is a gap-filler, not an override, so one unreadable node
	// shape cannot silently rewrite every other.
	GPUsPerNode int32

	// EmitNodes also emits the kwok Node manifests reproducing the fleet, so
	// that applying the output is enough to have the cluster.
	EmitNodes bool
}

// CaptureResult is the reproduction of a source cluster.
type CaptureResult struct {
	// Objects are the manifests to apply, in a safe order to apply them.
	Objects []client.Object

	// Warnings record everything the capture could not reproduce faithfully.
	// Capture is lossy by design; a lossy capture that says so is useful, and
	// one that stays quiet is misleading.
	Warnings []string
}

// Capture reproduces a source cluster's GPU fleet as ghostgpu manifests.
//
// It is a pure function of objects already read from the cluster, which is what
// makes the read-only guarantee checkable rather than merely claimed: there is
// no client here to write through. `ghostgpu capture` reads nodes and
// ResourceSlices and prints YAML — pointing a simulator at production has to be
// provably harmless, and that is the inverse of the safety invariant that keeps
// ghostgpu off nodes kwok does not manage.
//
// The reproduction is of *shape*, not of workloads: what hardware exists and
// how it is partitioned, not what is running on it.
func Capture(
	nodes []corev1.Node,
	published []resourcev1.ResourceSlice,
	opts CaptureOptions,
) (CaptureResult, error) {
	byNode := indexSlicesByNode(published)

	var result CaptureResult
	groups := map[string]*shapeGroup{}
	var order []string

	for i := range nodes {
		node := &nodes[i]

		shape, warnings, err := shapeOf(node, byNode[node.Name], opts)
		result.Warnings = append(result.Warnings, warnings...)
		if err != nil {
			return CaptureResult{}, err
		}
		if shape == nil {
			continue
		}

		key := shape.key()
		group, seen := groups[key]
		if !seen {
			group = &shapeGroup{shape: *shape}
			groups[key] = group
			order = append(order, key)
		}
		group.capacities = append(group.capacities, nodeCapacity(node))
	}

	if len(groups) == 0 {
		return result, fmt.Errorf(
			"no GPU nodes found: no node carries %s=%q. "+
				"Is GPU Feature Discovery running, and does this kubeconfig point at the right cluster?",
			gpu.LabelGPUPresent, labelTrue)
	}

	// Sorted so that the same cluster always renders the same YAML. Captured
	// output gets committed and diffed, which map iteration order would ruin.
	slices.Sort(order)

	result.Objects = assemble(groups, order, opts)

	if !opts.EmitNodes {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"nodes were not emitted: each pool selects on %s, so label your simulated nodes with it "+
				"or re-run without --nodes=false", ShapeLabel))
	}

	return result, nil
}

// nodeShape is one distinguishable kind of GPU node. Two nodes share a shape
// when the same GPUModel and GPUPool would reproduce either of them.
type nodeShape struct {
	product   string
	memory    resource.Quantity
	compute   string
	gpus      int32
	sharing   v1alpha1.SharingMode
	partition []v1alpha1.MIGPartitionEntry
	topology  v1alpha1.TopologySpec

	// profiles are only set for hardware ghostgpu has no built-in table for.
	profiles []v1alpha1.MIGProfileSpec
}

// shapeGroup is one shape and the nodes that had it.
//
// Per-node capacity is kept rather than folded into the shape because CPU and
// memory vary between machines of the same GPU shape, and a pool is about GPUs.
// Reproducing each node's own capacity keeps a fleet's heterogeneity without
// splintering it into a pool per machine.
type shapeGroup struct {
	shape      nodeShape
	capacities []corev1.ResourceList
}

// key identifies a shape for grouping. Everything the manifests would differ by
// has to appear here, or two genuinely different shapes would collapse into one.
func (s *nodeShape) key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%d|%s|%d|%t",
		s.product, s.memory.String(), s.compute, s.gpus, s.sharing,
		s.topology.NVLinkDomainSize, s.topology.NUMAAware)
	for _, e := range s.partition {
		fmt.Fprintf(&b, "|part:%s=%d", e.Profile, e.Count)
	}
	for _, p := range s.profiles {
		fmt.Fprintf(&b, "|prof:%s=%s/%d", p.Name, p.Memory.String(), p.Slices)
	}
	return b.String()
}

// modelKey identifies the hardware a shape runs, ignoring how much of it a node
// has or how it is divided. Those are pool concerns, so shapes differing only
// in them share one GPUModel — which is the API's own split, not a shortcut.
func (s *nodeShape) modelKey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s", s.product, s.memory.String(), s.compute)
	for _, p := range s.profiles {
		fmt.Fprintf(&b, "|prof:%s=%s/%d", p.Name, p.Memory.String(), p.Slices)
	}
	return b.String()
}

// assemble turns grouped shapes into named manifests.
func assemble(groups map[string]*shapeGroup, order []string, opts CaptureOptions) []client.Object {
	shapesPerModel := map[string]int{}
	for _, key := range order {
		shapesPerModel[groups[key].shape.modelKey()]++
	}

	models := make([]client.Object, 0, len(order))
	pools := make([]client.Object, 0, len(order))
	var simulated []client.Object

	modelNames := map[string]string{}
	modelNamer := newNamer()
	poolNamer := newNamer()

	for _, key := range order {
		shape := &groups[key].shape
		modelKey := shape.modelKey()

		name, built := modelNames[modelKey]
		if !built {
			name = modelNamer.pick(baseName(shape.product))
			modelNames[modelKey] = name
			models = append(models, buildModel(name, shape))
		}

		// A model with one shape gets the obvious "<model>-pool". Only when
		// several shapes share hardware do the names need to say what differs.
		base := name + "-pool"
		if shapesPerModel[modelKey] > 1 {
			base = fmt.Sprintf("%s-%dgpu", name, shape.gpus)
			if shape.sharing == v1alpha1.SharingModeMIG {
				base += "-mig"
			}
		}
		poolName := poolNamer.pick(base)

		pools = append(pools, buildPool(poolName, name, shape))
		if opts.EmitNodes {
			simulated = append(simulated, buildNodes(poolName, groups[key].capacities)...)
		}
	}

	objects := make([]client.Object, 0, len(models)+len(pools)+len(simulated))
	objects = append(objects, models...)
	objects = append(objects, pools...)
	objects = append(objects, simulated...)
	return objects
}

// shapeOf reads one node's shape, or nil if it has no GPUs to reproduce.
//
// A node missing a label the manifests genuinely need is skipped with a
// warning rather than filled in with a plausible value. An invented compute
// capability would be invisible in the output and would silently change what a
// selector matches; an absent pool is at least conspicuous.
func shapeOf(
	node *corev1.Node,
	published []*resourcev1.ResourceSlice,
	opts CaptureOptions,
) (*nodeShape, []string, error) {
	labels := node.Labels
	if labels[gpu.LabelGPUPresent] != labelTrue {
		return nil, nil, nil
	}

	skip := func(reason string) (*nodeShape, []string, error) {
		return nil, []string{fmt.Sprintf("node %s: %s; not reproduced", node.Name, reason)}, nil
	}

	product := labels[gpu.LabelGPUProduct]
	if product == "" {
		return skip(fmt.Sprintf("advertises GPUs but carries no %s label", gpu.LabelGPUProduct))
	}

	memory, ok := gpuMemory(labels)
	if !ok {
		return skip(fmt.Sprintf("%s is missing or unreadable", gpu.LabelGPUMemory))
	}

	compute, ok := computeCapability(labels)
	if !ok {
		return skip(fmt.Sprintf("%s/%s are missing", gpu.LabelComputeMajor, gpu.LabelComputeMinor))
	}

	var warnings []string

	migResources := migCapacity(node)
	strategy := labels[gpu.LabelMIGStrategy]

	// mig.capable means the hardware *could* be partitioned, not that it is;
	// treating it as "MIG is on" would partition every idle A100 in the fleet.
	// Only the mixed strategy, or MIG extended resources actually advertised,
	// says instances exist.
	partitioned := strategy == gpu.MIGStrategyMixed || len(migResources) > 0

	if strategy == migStrategySingle {
		warnings = append(warnings, fmt.Sprintf(
			"node %s uses the %q MIG strategy, which advertises instances as whole %s. "+
				"Captured as whole GPUs: that reproduces its scalar scheduling surface exactly, "+
				"but not MIG exclusivity. ghostgpu models the %q strategy",
			node.Name, migStrategySingle, gpu.GPUResourceName, gpu.MIGStrategyMixed))
	}

	gpus, err := physicalGPUs(node, published, partitioned, opts)
	if err != nil {
		return nil, warnings, err
	}

	shape := &nodeShape{
		product:  product,
		memory:   memory,
		compute:  compute,
		gpus:     gpus,
		sharing:  v1alpha1.SharingModeNone,
		topology: topologyOf(published),
	}

	if partitioned {
		shape.sharing = v1alpha1.SharingModeMIG

		if whole, err := strconv.Atoi(labels[gpu.LabelGPUCount]); err == nil && whole > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"node %s advertises %d unpartitioned GPUs alongside its MIG instances; "+
					"a ghostgpu pool partitions every GPU it manages, so the whole cards are not reproduced",
				node.Name, whole))
		}

		layout, layoutWarnings := migLayout(node.Name, product, memory, migResources, gpus)
		shape.partition = layout.partition
		shape.profiles = layout.profiles
		warnings = append(warnings, layoutWarnings...)
	}

	return shape, warnings, nil
}

// physicalGPUs works out how many cards a node has.
//
// Derivation is tried before the option because the option is fleet-wide: a
// single node shape that cannot be read must not silently rewrite the count of
// every other shape in the cluster.
func physicalGPUs(
	node *corev1.Node,
	published []*resourcev1.ResourceSlice,
	partitioned bool,
	opts CaptureOptions,
) (int32, error) {
	// One shared counter set per physical GPU is how both ghostgpu and NVIDIA's
	// DRA driver express a card's budget, so counting them is exact.
	if n := countCounterSets(published); n > 0 {
		return n, nil
	}

	// Under the mixed strategy gpu.count reports the cards left whole, which is
	// usually none, so it says nothing about how many exist. The same goes for
	// counting published devices: without MIG one device is one card, but with
	// it one card is many devices.
	if !partitioned {
		if n, err := strconv.Atoi(node.Labels[gpu.LabelGPUCount]); err == nil && n > 0 {
			return int32(n), nil
		}
		if n := countDevices(published); n > 0 {
			return n, nil
		}
	}

	if opts.GPUsPerNode > 0 {
		return opts.GPUsPerNode, nil
	}

	if partitioned {
		return 0, fmt.Errorf(
			"node %s: cannot tell how many physical GPUs it has. Under the %q MIG strategy %s reports the "+
				"unpartitioned cards, which is none, and no ResourceSlice publishes per-GPU counters. "+
				"Pass --gpus-per-node to say how many each node has",
			node.Name, gpu.MIGStrategyMixed, gpu.LabelGPUCount)
	}
	return 0, fmt.Errorf(
		"node %s: %s is missing or zero and no ResourceSlice describes its devices. "+
			"Pass --gpus-per-node to say how many each node has",
		node.Name, gpu.LabelGPUCount)
}

// migLayout is what a node's MIG extended resources say about each GPU.
type migLayoutResult struct {
	partition []v1alpha1.MIGPartitionEntry
	profiles  []v1alpha1.MIGProfileSpec
}

// migLayout derives the per-GPU partition, and the profile shapes when they
// cannot be looked up.
//
// MIG extended resources are the *mixed* strategy, which the device plugin
// publishes for instances an administrator pre-created. That is static MIG, so
// a partition is exactly the right thing to emit: it says these instances exist
// and no others, which is what makes the extended-resource projection exact
// rather than an overcommittable approximation.
func migLayout(
	nodeName, product string,
	memory resource.Quantity,
	resources map[string]int64,
	gpus int32,
) (migLayoutResult, []string) {
	var (
		out      migLayoutResult
		warnings []string
	)

	builtIn, known := mig.ProfilesFor(product)

	if len(resources) == 0 {
		if !known {
			warnings = append(warnings, fmt.Sprintf(
				"node %s runs MIG on %q, which ghostgpu has no profile table for, and advertises no %s* "+
					"resources to derive one from; set spec.migProfiles on the GPUModel by hand",
				nodeName, product, gpu.MIGResourcePrefix))
		}
		return out, warnings
	}

	for _, profile := range slices.Sorted(maps.Keys(resources)) {
		total := resources[profile]

		shape, parsed := mig.ParseProfileName(profile)
		if !parsed {
			warnings = append(warnings, fmt.Sprintf(
				"node %s advertises %s%s, whose name ghostgpu cannot read as a MIG profile; not reproduced",
				nodeName, gpu.MIGResourcePrefix, profile))
			continue
		}

		perGPU := total / int64(gpus)
		if perGPU < 1 {
			warnings = append(warnings, fmt.Sprintf(
				"node %s advertises %d %s%s across %d GPUs, fewer than one per GPU; "+
					"migPartition describes a uniform layout, so it is not reproduced",
				nodeName, total, gpu.MIGResourcePrefix, profile, gpus))
			continue
		}
		if total%int64(gpus) != 0 {
			warnings = append(warnings, fmt.Sprintf(
				"node %s advertises %d %s%s across %d GPUs, which is not uniform; "+
					"captured as %d per GPU (%d total)",
				nodeName, total, gpu.MIGResourcePrefix, profile, gpus, perGPU, perGPU*int64(gpus)))
		}
		if perGPU > maxInstancesPerGPU {
			warnings = append(warnings, fmt.Sprintf(
				"node %s implies %d %s%s per GPU, more than a MIG-capable card can hold; capped at %d",
				nodeName, perGPU, gpu.MIGResourcePrefix, profile, maxInstancesPerGPU))
			perGPU = maxInstancesPerGPU
		}

		out.partition = append(out.partition, v1alpha1.MIGPartitionEntry{
			Profile: profile,
			Count:   int32(perGPU),
		})
		if !known {
			out.profiles = append(out.profiles, v1alpha1.MIGProfileSpec{
				Name:   shape.Name,
				Memory: shape.Memory,
				Slices: shape.Slices,
			})
		}
	}

	// Validated against the same budget the operator will use, so a layout the
	// operator would reject with MIGPartitionInvalid is called out here rather
	// than discovered after applying. It stays a warning: the source cluster is
	// the authority on what its hardware can do, not ghostgpu's table.
	table := builtIn
	if !known {
		table = mig.Table{
			Budget:   mig.Budget{Memory: memory, Slices: defaultMIGSlices},
			Profiles: toMIGProfiles(out.profiles),
		}
	}
	if err := mig.ValidatePartition(out.partition, table); err != nil {
		warnings = append(warnings, fmt.Sprintf(
			"node %s: the captured MIG layout does not fit one GPU as ghostgpu models it, so the operator "+
				"will reject it: %v", nodeName, err))
	}

	return out, warnings
}

// defaultMIGSlices is the compute-slice count of every MIG-capable NVIDIA GPU
// to date, used to budget hardware ghostgpu has no table for. It mirrors the
// mig package's own default.
const defaultMIGSlices = 7

// migStrategySingle is NVIDIA's other MIG strategy, which ghostgpu does not
// model: it advertises every instance as a whole nvidia.com/gpu and encodes the
// profile in the product name.
const migStrategySingle = "single"

// topologyOf recovers the interconnect shape from published devices. No node
// label carries it, so a cluster without DRA reports none — which is the honest
// answer, and better than inventing a domain size.
func topologyOf(published []*resourcev1.ResourceSlice) v1alpha1.TopologySpec {
	domains := map[string]map[string]bool{}
	numaAware := false

	for _, slice := range published {
		for _, device := range slice.Spec.Devices {
			if attr, ok := device.Attributes[gpu.AttrNUMANode]; ok && attr.IntValue != nil {
				numaAware = true
			}

			attr, ok := device.Attributes[gpu.AttrNVLinkDomain]
			if !ok || attr.StringValue == nil {
				continue
			}

			// Count physical cards, not devices: under MIG many instances share
			// one GPU, and they carry that GPU's uuid.
			identity := device.Name
			if uuid, ok := device.Attributes[gpu.AttrUUID]; ok && uuid.StringValue != nil {
				identity = *uuid.StringValue
			}

			members, seen := domains[*attr.StringValue]
			if !seen {
				members = map[string]bool{}
				domains[*attr.StringValue] = members
			}
			members[identity] = true
		}
	}

	// The largest domain is the domain size. Domains are assigned by dividing
	// the GPU index, so the first is always full whenever the node has at least
	// one domain's worth of cards; when it has fewer, every card lands in one
	// domain and its size reproduces that same grouping.
	var size int32
	for _, members := range domains {
		size = max(size, int32(len(members)))
	}

	return v1alpha1.TopologySpec{NVLinkDomainSize: size, NUMAAware: numaAware}
}

func indexSlicesByNode(published []resourcev1.ResourceSlice) map[string][]*resourcev1.ResourceSlice {
	byNode := map[string][]*resourcev1.ResourceSlice{}
	for i := range published {
		slice := &published[i]
		if slice.Spec.NodeName == nil || *slice.Spec.NodeName == "" {
			continue
		}
		byNode[*slice.Spec.NodeName] = append(byNode[*slice.Spec.NodeName], slice)
	}
	return byNode
}

func countDevices(published []*resourcev1.ResourceSlice) int32 {
	var total int32
	for _, slice := range published {
		total += int32(len(slice.Spec.Devices))
	}
	return total
}

func countCounterSets(published []*resourcev1.ResourceSlice) int32 {
	names := map[string]bool{}
	for _, slice := range published {
		for _, set := range slice.Spec.SharedCounters {
			names[set.Name] = true
		}
	}
	return int32(len(names))
}

// migCapacity reads the MIG instances a node advertises, keyed by profile.
func migCapacity(node *corev1.Node) map[string]int64 {
	instances := map[string]int64{}
	for name, quantity := range node.Status.Capacity {
		profile, ok := strings.CutPrefix(string(name), gpu.MIGResourcePrefix)
		if !ok {
			continue
		}
		if count := quantity.Value(); count > 0 {
			instances[profile] = count
		}
	}
	return instances
}

// gpuMemory reads GFD's memory label, which is a bare MiB count.
func gpuMemory(labels map[string]string) (resource.Quantity, bool) {
	mib, err := strconv.ParseInt(labels[gpu.LabelGPUMemory], 10, 64)
	if err != nil || mib <= 0 {
		return resource.Quantity{}, false
	}
	return *resource.NewQuantity(mib*1024*1024, resource.BinarySI), true
}

func computeCapability(labels map[string]string) (string, bool) {
	major, minor := labels[gpu.LabelComputeMajor], labels[gpu.LabelComputeMinor]
	if major == "" || minor == "" {
		return "", false
	}
	capability := major + "." + minor
	if !computeCapabilityPattern.MatchString(capability) {
		return "", false
	}
	return capability, true
}

// capturedNodeResources is the allow-list of node capacity worth reproducing.
//
// An allow-list rather than a copy-with-exclusions: node capacity can carry
// arbitrary vendor extended resources, and captured output is exactly what ends
// up pasted into a public issue.
var capturedNodeResources = []corev1.ResourceName{
	corev1.ResourceCPU,
	corev1.ResourceMemory,
	corev1.ResourceEphemeralStorage,
	corev1.ResourcePods,
}

func nodeCapacity(node *corev1.Node) corev1.ResourceList {
	capacity := corev1.ResourceList{}
	for _, name := range capturedNodeResources {
		if quantity, ok := node.Status.Capacity[name]; ok {
			capacity[name] = quantity.DeepCopy()
		}
	}
	if _, ok := capacity[corev1.ResourcePods]; !ok {
		capacity[corev1.ResourcePods] = *resource.NewQuantity(defaultPods, resource.DecimalSI)
	}
	return capacity
}

func buildModel(name string, shape *nodeShape) *v1alpha1.GPUModel {
	return &v1alpha1.GPUModel{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       kindGPUModel,
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUModelSpec{
			Vendor:            "nvidia",
			ProductName:       shape.product,
			Memory:            shape.memory,
			ComputeCapability: shape.compute,
			// Left empty for hardware ghostgpu recognises, so the operator
			// resolves its own table rather than freezing today's numbers into
			// the captured manifest.
			MIGProfiles: shape.profiles,
		},
	}
}

func buildPool(name, modelRef string, shape *nodeShape) *v1alpha1.GPUPool {
	return &v1alpha1.GPUPool{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       kindGPUPool,
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.GPUPoolSpec{
			ModelRef:     modelRef,
			NodeSelector: map[string]string{ShapeLabel: name},
			GPUsPerNode:  shape.gpus,
			SharingMode:  shape.sharing,
			MIGPartition: shape.partition,
			Topology:     shape.topology,
			// advertise is deliberately omitted so both paths default on. The
			// source cluster's advertisement style says what its tooling reads
			// today, not what the user wants to test against tomorrow, and
			// publishing DRA alongside costs nothing.
		},
	}
}

// buildNodes reproduces one pool's nodes as kwok nodes.
//
// Names are synthesised rather than copied. Real node names carry internal
// hostnames and topology, and captured output is meant to be shareable — an
// anonymised name loses nothing that a simulation needs.
func buildNodes(poolName string, capacities []corev1.ResourceList) []client.Object {
	prefix := strings.TrimSuffix(poolName, "-pool")

	nodes := make([]client.Object, 0, len(capacities))
	for i, capacity := range capacities {
		name := fmt.Sprintf("%s-%d", prefix, i)
		nodes = append(nodes, &corev1.Node{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Annotations: map[string]string{
					// Without this ghostgpu refuses to touch the node, and kwok
					// never adopts it.
					kwokNodeAnnotation: kwokNodeValue,
					ttlAnnotation:      "0",
				},
				Labels: map[string]string{
					"kubernetes.io/arch":     "amd64",
					"kubernetes.io/os":       "linux",
					"kubernetes.io/hostname": name,
					"kubernetes.io/role":     "agent",
					nodeTypeLabel:            nodeTypeKwok,
					ShapeLabel:               poolName,
				},
			},
			Spec: corev1.NodeSpec{
				// kwok's convention: real workloads do not drift onto simulated
				// hardware unless they say they tolerate it.
				Taints: []corev1.Taint{{
					Key:    kwokNodeAnnotation,
					Value:  kwokNodeValue,
					Effect: corev1.TaintEffectNoSchedule,
				}},
			},
			Status: corev1.NodeStatus{
				Capacity:    capacity,
				Allocatable: capacity,
				NodeInfo: corev1.NodeSystemInfo{
					Architecture:            "amd64",
					ContainerRuntimeVersion: "containerd://fake",
					KubeletVersion:          "fake",
					OperatingSystem:         "linux",
				},
				Phase: corev1.NodeRunning,
			},
		})
	}
	return nodes
}

// baseName turns a GPU product name into a Kubernetes object name.
//
// The vendor prefix is dropped because every name would carry it, and a pool
// called "nvidia-h100-80gb-hbm3-pool" is longer without being clearer.
func baseName(product string) string {
	var b strings.Builder
	separated := false

	for _, r := range strings.ToLower(product) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			separated = false
			continue
		}
		if !separated && b.Len() > 0 {
			b.WriteByte('-')
			separated = true
		}
	}

	name := strings.Trim(b.String(), "-")
	name = strings.TrimPrefix(name, "nvidia-")
	if len(name) > maxNameBase {
		name = strings.Trim(name[:maxNameBase], "-")
	}
	if name == "" {
		name = "gpu"
	}
	return name
}

// namer hands out names that do not collide.
type namer struct{ taken map[string]bool }

func newNamer() *namer { return &namer{taken: map[string]bool{}} }

func (n *namer) pick(base string) string {
	if !n.taken[base] {
		n.taken[base] = true
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !n.taken[candidate] {
			n.taken[candidate] = true
			return candidate
		}
	}
}
