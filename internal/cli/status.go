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
	"cmp"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
)

// Counter names ghostgpu publishes on a GPU's shared counter set.
const (
	counterSlices = "slices"
	counterMemory = "memory"
)

// PoolStatus is one pool's headline numbers.
type PoolStatus struct {
	Name      string
	Mode      string
	Nodes     int32
	Devices   int32
	Allocated int
}

// DeviceStatus is one published device and who holds it.
type DeviceStatus struct {
	Node      string
	Pool      string
	Device    string
	Profile   string
	Allocated bool
	Pod       string
	Namespace string
}

// GPUStatus is one physical GPU's budget and how much of it is spoken for.
//
// This is the expensive thing to work out by hand: the consumption of every
// allocated instance has to be summed against a budget declared in a different
// ResourceSlice, and it is the answer to "why can nothing else fit on this
// card".
type GPUStatus struct {
	Node        string
	CounterSet  string
	UsedSlices  int64
	TotalSlices int64
	UsedMemory  resource.Quantity
	TotalMemory resource.Quantity
}

// Report is everything `ghostgpu status` can show.
type Report struct {
	Pools   []PoolStatus
	Devices []DeviceStatus
	GPUs    []GPUStatus
}

// allocation identifies one allocated device and its holder.
type allocation struct {
	pod       string
	namespace string
}

// deviceKey identifies a device within the cluster. The DRA pool name is the
// node name in ghostgpu's layout, but device names are only unique within a
// pool, so both are needed.
type deviceKey struct {
	pool   string
	device string
}

// BuildReport derives the whole report from objects already in the cluster.
//
// Nothing here is stored by ghostgpu: allocation lives in ResourceClaim.status,
// which the scheduler owns, and the published devices live in ResourceSlices.
// Deriving rather than tracking is what keeps this from becoming a second
// source of truth that can disagree with the first.
func BuildReport(
	pools []v1alpha1.GPUPool,
	published []resourcev1.ResourceSlice,
	claims []resourcev1.ResourceClaim,
) Report {
	allocated := indexAllocations(claims)

	report := Report{
		Pools:   summarisePools(pools, published, allocated),
		Devices: describeDevices(published, allocated),
		GPUs:    describeGPUs(published, allocated),
	}
	return report
}

// indexAllocations maps each allocated ghostgpu device to its holder.
//
// Results for other drivers are skipped. A cluster running a real GPU driver
// alongside ghostgpu would otherwise show phantom holders on simulated devices.
func indexAllocations(claims []resourcev1.ResourceClaim) map[deviceKey]allocation {
	held := map[deviceKey]allocation{}

	for i := range claims {
		claim := &claims[i]
		if claim.Status.Allocation == nil {
			continue
		}

		var holder allocation
		for _, consumer := range claim.Status.ReservedFor {
			if consumer.Resource == "pods" {
				holder = allocation{pod: consumer.Name, namespace: claim.Namespace}
				break
			}
		}

		for _, result := range claim.Status.Allocation.Devices.Results {
			if result.Driver != gpu.DriverName {
				continue
			}
			held[deviceKey{pool: result.Pool, device: result.Device}] = holder
		}
	}
	return held
}

func summarisePools(
	pools []v1alpha1.GPUPool,
	published []resourcev1.ResourceSlice,
	allocated map[deviceKey]allocation,
) []PoolStatus {
	// Which pool each slice belongs to, so allocations can be attributed.
	poolOf := map[deviceKey]string{}
	for i := range published {
		slice := &published[i]
		owner := slice.Labels[poolLabel]
		for _, d := range slice.Spec.Devices {
			poolOf[deviceKey{pool: slice.Spec.Pool.Name, device: d.Name}] = owner
		}
	}

	counts := map[string]int{}
	for key := range allocated {
		if owner, ok := poolOf[key]; ok {
			counts[owner]++
		}
	}

	out := make([]PoolStatus, 0, len(pools))
	for i := range pools {
		p := &pools[i]
		mode := string(p.Spec.SharingMode)
		if mode == "" {
			mode = string(v1alpha1.SharingModeNone)
		}
		out = append(out, PoolStatus{
			Name:      p.Name,
			Mode:      mode,
			Nodes:     p.Status.NodesMatched,
			Devices:   p.Status.DevicesPublished,
			Allocated: counts[p.Name],
		})
	}

	slices.SortFunc(out, func(a, b PoolStatus) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func describeDevices(
	published []resourcev1.ResourceSlice,
	allocated map[deviceKey]allocation,
) []DeviceStatus {
	var out []DeviceStatus

	for i := range published {
		slice := &published[i]
		for _, d := range slice.Spec.Devices {
			key := deviceKey{pool: slice.Spec.Pool.Name, device: d.Name}
			holder, held := allocated[key]

			status := DeviceStatus{
				Node:      slice.Spec.Pool.Name,
				Pool:      slice.Labels[poolLabel],
				Device:    d.Name,
				Allocated: held,
				Pod:       holder.pod,
				Namespace: holder.namespace,
			}
			if attr, ok := d.Attributes[gpu.AttrMIGProfile]; ok && attr.StringValue != nil {
				status.Profile = *attr.StringValue
			}
			out = append(out, status)
		}
	}

	slices.SortFunc(out, func(a, b DeviceStatus) int {
		if n := cmp.Compare(a.Node, b.Node); n != 0 {
			return n
		}
		return cmp.Compare(a.Device, b.Device)
	})
	return out
}

// describeGPUs sums what each physical GPU's budget is carrying.
func describeGPUs(
	published []resourcev1.ResourceSlice,
	allocated map[deviceKey]allocation,
) []GPUStatus {
	type budgetKey struct{ node, set string }
	budgets := map[budgetKey]*GPUStatus{}

	for i := range published {
		slice := &published[i]
		for _, set := range slice.Spec.SharedCounters {
			status := &GPUStatus{
				Node:        slice.Spec.Pool.Name,
				CounterSet:  set.Name,
				UsedMemory:  *resource.NewQuantity(0, resource.BinarySI),
				TotalMemory: *resource.NewQuantity(0, resource.BinarySI),
			}
			if c, ok := set.Counters[counterSlices]; ok {
				status.TotalSlices = c.Value.Value()
			}
			if c, ok := set.Counters[counterMemory]; ok {
				status.TotalMemory = c.Value.DeepCopy()
			}
			budgets[budgetKey{slice.Spec.Pool.Name, set.Name}] = status
		}
	}

	// Only allocated devices draw down the budget. A published-but-free
	// instance consumes nothing, which is the whole point of counters.
	for i := range published {
		slice := &published[i]
		for _, d := range slice.Spec.Devices {
			if _, held := allocated[deviceKey{pool: slice.Spec.Pool.Name, device: d.Name}]; !held {
				continue
			}
			for _, consumption := range d.ConsumesCounters {
				status, ok := budgets[budgetKey{slice.Spec.Pool.Name, consumption.CounterSet}]
				if !ok {
					continue
				}
				if c, ok := consumption.Counters[counterSlices]; ok {
					status.UsedSlices += c.Value.Value()
				}
				if c, ok := consumption.Counters[counterMemory]; ok {
					status.UsedMemory.Add(c.Value)
				}
			}
		}
	}

	out := make([]GPUStatus, 0, len(budgets))
	for _, status := range budgets {
		out = append(out, *status)
	}
	slices.SortFunc(out, func(a, b GPUStatus) int {
		if n := cmp.Compare(a.Node, b.Node); n != 0 {
			return n
		}
		return cmp.Compare(a.CounterSet, b.CounterSet)
	})
	return out
}

// poolLabel marks a ResourceSlice as managed by a named GPUPool. It mirrors the
// controller's constant; importing the controller here would drag the whole
// manager into the CLI binary.
const poolLabel = "ghostgpu.dev/pool"

func table(write func(*tabwriter.Writer)) string {
	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	write(w)
	_ = w.Flush()
	return buf.String()
}

// RenderPools renders the per-pool summary.
func RenderPools(report Report) string {
	return table(func(w *tabwriter.Writer) {
		_, _ = fmt.Fprintln(w, "POOL\tMODE\tNODES\tDEVICES\tALLOCATED\tFREE")
		for _, p := range report.Pools {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\n",
				p.Name, p.Mode, p.Nodes, p.Devices, p.Allocated, int(p.Devices)-p.Allocated)
		}
	})
}

// RenderDevices renders one node's devices, or every node when node is empty.
func RenderDevices(report Report, node string) string {
	return table(func(w *tabwriter.Writer) {
		_, _ = fmt.Fprintln(w, "NODE\tDEVICE\tPROFILE\tSTATUS\tPOD")
		for _, d := range report.Devices {
			if node != "" && d.Node != node {
				continue
			}

			profile := d.Profile
			if profile == "" {
				profile = "-"
			}

			status, holder := "free", "-"
			if d.Allocated {
				status = "allocated"
				// An allocated claim with no consumer yet is genuinely
				// different from a free device, so it says so rather than
				// leaving a blank that reads as free.
				holder = "(unreserved)"
				if d.Pod != "" {
					holder = d.Namespace + "/" + d.Pod
				}
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Node, d.Device, profile, status, holder)
		}
	})
}

// RenderGPUs renders per-GPU budget usage, which is what explains why a MIG
// instance cannot be allocated.
func RenderGPUs(report Report, node string) string {
	return table(func(w *tabwriter.Writer) {
		_, _ = fmt.Fprintln(w, "NODE\tGPU\tSLICES\tMEMORY")
		for _, g := range report.GPUs {
			if node != "" && g.Node != node {
				continue
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%d/%d\t%s/%s\n",
				g.Node, g.CounterSet,
				g.UsedSlices, g.TotalSlices,
				g.UsedMemory.String(), g.TotalMemory.String())
		}
	})
}
