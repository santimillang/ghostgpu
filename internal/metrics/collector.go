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

package metrics

import (
	"strconv"
	"strings"

	resourcev1 "k8s.io/api/resource/v1"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
)

// PoolLabel marks a ResourceSlice as managed by a named GPUPool. It mirrors the
// controller's constant rather than importing it, which would pull the whole
// manager into anything that reads metrics.
const PoolLabel = "ghostgpu.dev/pool"

// Allocation is one device's holder, keyed by DRA pool and device name.
type Allocation struct {
	Pool   string
	Device string
}

// Workloads maps a pod to its labels, so a utilization profile can be matched
// against the job actually holding a GPU.
type Workloads map[Holder]map[string]string

// CardsFrom assembles simulated GPU state from objects already in the cluster.
//
// Derived, never stored — the same principle `ghostgpu status` follows. The
// devices come from the ResourceSlices ghostgpu published, the holders from
// ResourceClaim.status which the scheduler owns, and the readings from the
// pool's declared utilization. Nothing here is a second source of truth that
// could drift from the first.
func CardsFrom(
	pools []v1alpha1.GPUPool,
	models map[string]*v1alpha1.GPUModel,
	published []resourcev1.ResourceSlice,
	holders map[Allocation]Holder,
	workloads Workloads,
) []Card {
	specs := make(map[string]*v1alpha1.GPUPool, len(pools))
	for i := range pools {
		specs[pools[i].Name] = &pools[i]
	}

	// Keyed by node and physical card, because a MIG card's instances arrive
	// spread across several device slices.
	type cardKey struct {
		node  string
		index int32
	}
	cards := map[cardKey]*Card{}
	var order []cardKey

	for i := range published {
		slice := &published[i]
		pool := specs[slice.Labels[PoolLabel]]
		if pool == nil {
			// A slice from another driver, or one whose pool has gone. Not
			// ghostgpu's to describe.
			continue
		}

		node := slice.Spec.Pool.Name
		for _, device := range slice.Spec.Devices {
			index, ok := cardIndex(device.Name)
			if !ok {
				continue
			}

			key := cardKey{node, index}
			card := cards[key]
			if card == nil {
				card = &Card{
					Node:        node,
					Index:       index,
					UUID:        stringAttr(device, gpu.AttrUUID),
					ProductName: stringAttr(device, gpu.AttrProductName),
				}
				if model := models[pool.Spec.ModelRef]; model != nil {
					card.MemoryMiB = model.Spec.Memory.Value() / mib
				}
				cards[key] = card
				order = append(order, key)
			}

			holder, held := holders[Allocation{Pool: node, Device: device.Name}]
			// A device ghostgpu declared occupied has no holder but is just as
			// unavailable, and reporting it idle would contradict the fleet the
			// user asked for.
			busy := held || occupied(device)

			// A failed GPU reports its XID, which is what remediation watches,
			// and reports no utilization: broken hardware is not doing work.
			xid, faulted := faultedXID(device)
			if faulted {
				busy = false
			}

			reading := Resolve(pool.Spec.Utilization, busy, workloads[holder])
			reading.XID = xid

			if profile := stringAttr(device, gpu.AttrMIGProfile); profile != "" {
				card.Instances = append(card.Instances, Instance{
					Profile:    profile,
					InstanceID: intAttr(device, gpu.AttrMIGInstanceID),
					MemoryMiB:  deviceMemoryMiB(device),
					Holder:     holder,
					Reading:    reading,
				})
				// The card's own reading is filled in below from its instances.
				continue
			}

			card.Holder = holder
			card.Reading = reading
		}
	}

	out := make([]Card, 0, len(order))
	for _, key := range order {
		card := cards[key]
		if len(card.Instances) > 0 {
			summarizeCard(card)
		}
		out = append(out, *card)
	}
	return out
}

// summarizeCard gives a partitioned card a reading of its own.
//
// dcgm-exporter reports the whole GPU as well as each instance, so the card
// needs a number. Utilization is the busiest instance rather than a mean:
// "this card is 100% busy" is what a card carrying one saturated instance
// actually looks like to anything watching for idle hardware, and averaging
// would make a mostly-idle card indistinguishable from a fully idle one.
// Framebuffer is the sum, because that genuinely adds up.
func summarizeCard(card *Card) {
	var usedMiB int64
	for _, instance := range card.Instances {
		card.Reading.GPUUtil = max(card.Reading.GPUUtil, instance.Reading.GPUUtil)
		card.Reading.MemCopyUtil = max(card.Reading.MemCopyUtil, instance.Reading.MemCopyUtil)
		card.Reading.GREngineActive = max(card.Reading.GREngineActive, instance.Reading.GREngineActive)
		card.Reading.TensorActive = max(card.Reading.TensorActive, instance.Reading.TensorActive)

		usedMiB += instance.MemoryMiB * int64(instance.Reading.FBUsedPercent) / 100

		// Physical readings are the card's, so any instance's declared value
		// serves; they all resolve from the same pool spec.
		if card.Reading.PowerWatts == nil {
			card.Reading.PowerWatts = instance.Reading.PowerWatts
		}
		if card.Reading.TemperatureC == nil {
			card.Reading.TemperatureC = instance.Reading.TemperatureC
		}
	}

	if card.MemoryMiB > 0 {
		card.Reading.FBUsedPercent = int32(usedMiB * 100 / card.MemoryMiB)
	}
}

// mib converts a resource.Quantity's bytes into the MiB DCGM reports.
const mib = 1024 * 1024

// cardIndex recovers the physical GPU index from a device name. ghostgpu names
// devices "gpu-<index>" and MIG instances "gpu-<index>-<profile>", so the index
// is the second dash-separated field in both.
func cardIndex(deviceName string) (int32, bool) {
	rest, ok := strings.CutPrefix(deviceName, "gpu-")
	if !ok {
		return 0, false
	}
	digits, _, _ := strings.Cut(rest, "-")

	index, err := strconv.Atoi(digits)
	if err != nil || index < 0 {
		return 0, false
	}
	return int32(index), true
}

func occupied(device resourcev1.Device) bool {
	for _, taint := range device.Taints {
		if taint.Key == gpu.OccupiedTaintKey {
			return true
		}
	}
	return false
}

// faultedXID reads the failure ghostgpu declared for a device.
//
// The XID travels on the taint value as "xid-<n>", so the ResourceSlice alone
// explains why a device is out of service. Reading it back from there rather
// than from the pool spec means what is reported is what the scheduler is
// acting on.
func faultedXID(device resourcev1.Device) (int32, bool) {
	for _, taint := range device.Taints {
		if taint.Key != gpu.FaultedTaintKey {
			continue
		}
		digits, ok := strings.CutPrefix(taint.Value, "xid-")
		if !ok {
			// A fault with no XID declared: out of service, driver silent.
			return 0, true
		}
		xid, err := strconv.Atoi(digits)
		if err != nil {
			return 0, true
		}
		return int32(xid), true
	}
	return 0, false
}

func stringAttr(device resourcev1.Device, name resourcev1.QualifiedName) string {
	if attr, ok := device.Attributes[name]; ok && attr.StringValue != nil {
		return *attr.StringValue
	}
	return ""
}

func intAttr(device resourcev1.Device, name resourcev1.QualifiedName) int32 {
	if attr, ok := device.Attributes[name]; ok && attr.IntValue != nil {
		return int32(*attr.IntValue)
	}
	return 0
}

func deviceMemoryMiB(device resourcev1.Device) int64 {
	if capacity, ok := device.Capacity["memory"]; ok {
		return capacity.Value.Value() / mib
	}
	return 0
}
