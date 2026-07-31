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
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

// Reading is one resolved set of values for a GPU or MIG instance.
//
// Resolved rather than declared: the spec's fields are optional pointers, and
// this is what they came out as once defaults were applied.
type Reading struct {
	GPUUtil        int32
	MemCopyUtil    int32
	FBUsedPercent  int32
	GREngineActive int32
	TensorActive   int32

	// Nil means the series is not emitted at all. ghostgpu has no thermal or
	// power model, and a plausible-looking wattage would be fabrication rather
	// than simulation.
	PowerWatts   *int32
	TemperatureC *int32
}

// Holder is the workload a device belongs to, as the scheduler recorded it.
type Holder struct {
	Namespace string
	Pod       string
	Container string
}

// Instance is one MIG instance carved from a card.
type Instance struct {
	Profile    string
	InstanceID int32
	MemoryMiB  int64
	Holder     Holder
	Reading    Reading
}

// Card is one simulated physical GPU.
type Card struct {
	Node        string
	Index       int32
	UUID        string
	ProductName string
	MemoryMiB   int64
	Holder      Holder
	Reading     Reading

	// Instances is empty unless the pool partitions this card.
	Instances []Instance
}

// Series is one Prometheus sample.
type Series struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// physical metrics describe the card itself, so they are never emitted per MIG
// instance: instances share one piece of silicon, and a per-instance wattage
// would be a number no hardware could produce. This matches dcgm-exporter,
// which reports power, temperature and clocks for the whole GPU only.

// Build turns simulated GPU state into DCGM-shaped samples.
//
// Every card produces a full set of card-level series. A partitioned card
// additionally produces per-instance series for the readings that are genuinely
// per-instance, carrying GPU_I_ID and GPU_I_PROFILE — which is what
// dcgm-exporter does under MIG, reporting both levels rather than one.
//
// Output is sorted, because metrics output gets diffed in tests and map
// iteration order would make that useless.
func Build(cards []Card) []Series {
	var out []Series

	for i := range cards {
		card := &cards[i]
		base := cardLabels(card)

		out = append(out, utilizationSeries(base, card.Reading, card.MemoryMiB)...)

		// Physical readings, card level only.
		if card.Reading.PowerWatts != nil {
			out = append(out, Series{PowerUsage, float64(*card.Reading.PowerWatts), base})
		}
		if card.Reading.TemperatureC != nil {
			out = append(out, Series{GPUTemp, float64(*card.Reading.TemperatureC), base})
		}
		out = append(out, Series{XIDErrors, 0, base})

		for _, instance := range card.Instances {
			labels := instanceLabels(card, instance)
			out = append(out, utilizationSeries(labels, instance.Reading, instance.MemoryMiB)...)
			out = append(out, Series{XIDErrors, 0, labels})
		}
	}

	slices.SortFunc(out, func(a, b Series) int {
		if n := cmp.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return cmp.Compare(labelKey(a.Labels), labelKey(b.Labels))
	})
	return out
}

// utilizationSeries are the readings that mean something for a whole card and
// for a single MIG instance alike.
func utilizationSeries(labels map[string]string, r Reading, memoryMiB int64) []Series {
	used := memoryMiB * int64(r.FBUsedPercent) / 100

	return []Series{
		{GPUUtil, float64(r.GPUUtil), labels},
		{MemCopyUtil, float64(r.MemCopyUtil), labels},
		{FBUsed, float64(used), labels},
		// Derived rather than declared separately, so used and free always sum
		// to the framebuffer the GPUModel says the card has.
		{FBFree, float64(memoryMiB - used), labels},
		// DCGM reports these as ratios; the spec takes percentages because a
		// percentage is easier to write and read in a manifest.
		{GREngineActive, float64(r.GREngineActive) / 100, labels},
		{TensorActive, float64(r.TensorActive) / 100, labels},
	}
}

func cardLabels(card *Card) map[string]string {
	labels := map[string]string{
		LabelGPU:       strconv.Itoa(int(card.Index)),
		LabelUUID:      card.UUID,
		LabelDevice:    fmt.Sprintf("nvidia%d", card.Index),
		LabelModelName: card.ProductName,
		LabelHostname:  card.Node,
	}
	addHolder(labels, card.Holder)
	return labels
}

func instanceLabels(card *Card, instance Instance) map[string]string {
	labels := map[string]string{
		LabelGPU:       strconv.Itoa(int(card.Index)),
		LabelUUID:      card.UUID,
		LabelDevice:    fmt.Sprintf("nvidia%d", card.Index),
		LabelModelName: card.ProductName,
		LabelHostname:  card.Node,

		LabelMIGInstanceID: strconv.Itoa(int(instance.InstanceID)),
		LabelMIGProfile:    instance.Profile,
	}
	addHolder(labels, instance.Holder)
	return labels
}

// addHolder attaches the workload labels, and omits them entirely when nothing
// holds the device.
//
// Omitting rather than writing empty strings is deliberate: an empty pod label
// is a distinct time series that queries like sum by (pod) would happily group
// on, which is exactly the confusion the real exporter's bug reports describe.
func addHolder(labels map[string]string, holder Holder) {
	if holder.Pod == "" {
		return
	}
	labels[LabelNamespace] = holder.Namespace
	labels[LabelPod] = holder.Pod
	if holder.Container != "" {
		labels[LabelContainer] = holder.Container
	}
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var key strings.Builder
	for _, k := range keys {
		key.WriteString(k)
		key.WriteByte('=')
		key.WriteString(labels[k])
		key.WriteByte(',')
	}
	return key.String()
}

// Resolve turns a declared utilization spec into the readings a device in a
// given state reports.
//
// Idle defaults to zero across the board, which is a fact about an unused GPU
// rather than a guess. Busy defaults to fully utilized. Power and temperature
// have no default in either state: ghostgpu models neither, so those series
// stay absent until someone declares them.
func Resolve(spec *v1alpha1.UtilizationSpec, busy bool) Reading {
	reading := Reading{}
	if busy {
		reading = Reading{GPUUtil: 100, FBUsedPercent: 100, GREngineActive: 100}
	}

	if spec == nil {
		return reading
	}

	sample := spec.WhenIdle
	if busy {
		sample = spec.WhenAllocated
	}
	if sample == nil {
		return reading
	}

	// Every field is a pointer so that an explicit zero overrides a non-zero
	// default. Zero is a meaningful reading here, not an absence.
	setIf(&reading.GPUUtil, sample.GPUUtil)
	setIf(&reading.MemCopyUtil, sample.MemCopyUtil)
	setIf(&reading.FBUsedPercent, sample.FBUsedPercent)
	setIf(&reading.GREngineActive, sample.GREngineActivePercent)
	setIf(&reading.TensorActive, sample.TensorActivePercent)
	reading.PowerWatts = sample.PowerWatts
	reading.TemperatureC = sample.TemperatureC

	return reading
}

func setIf(target *int32, value *int32) {
	if value != nil {
		*target = *value
	}
}
