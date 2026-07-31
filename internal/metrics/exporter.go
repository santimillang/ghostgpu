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
	"context"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/gpu"
)

// help text per metric, so the exposition carries the same descriptions
// dcgm-exporter publishes.
var help = map[string]string{
	GPUUtil:        "GPU utilization (in %).",
	MemCopyUtil:    "Memory utilization (in %).",
	FBUsed:         "Framebuffer memory used (in MiB).",
	FBFree:         "Framebuffer memory free (in MiB).",
	GREngineActive: "Ratio of time the graphics engine is active.",
	TensorActive:   "Ratio of cycles the tensor (HMMA) pipe is active.",
	PowerUsage:     "Power draw (in W).",
	GPUTemp:        "GPU temperature (in C).",
	XIDErrors:      "Value of the last XID error encountered.",
}

// podsResource is the ResourceClaim consumer kind that names a workload. A
// claim can be reserved by other things; only a pod is a holder worth
// attributing a GPU to.
const podsResource = "pods"

// Exporter is a Prometheus collector publishing the simulated fleet.
//
// It reads on every scrape rather than maintaining state. That keeps the
// numbers consistent with the cluster by construction — there is no cache to go
// stale, and no second source of truth about who holds which GPU — at the cost
// of a few list calls per scrape, which is the right trade for a simulator.
//
// The client is a Reader: this path only ever reads, and the type says so.
type Exporter struct {
	Reader client.Reader
}

var _ prometheus.Collector = &Exporter{}

// Describe is deliberately empty, making this an unchecked collector.
//
// The label set genuinely varies between series — an idle device carries no pod
// labels, a MIG instance carries GPU_I_ID — and a checked collector requires
// every series of a metric to share one label signature.
func (e *Exporter) Describe(chan<- *prometheus.Desc) {}

// Collect emits the current fleet.
//
// A read failure yields no samples rather than stale or partial ones. Prometheus
// records the scrape as failed, which is a truthful signal; inventing numbers
// for a cluster that could not be read would be the one failure mode a
// simulator must never have.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()

	cards, err := e.snapshot(ctx)
	if err != nil {
		logf.Log.WithName("metrics").Error(err, "collecting simulated GPU metrics")
		return
	}

	for _, series := range Build(cards) {
		desc := prometheus.NewDesc(series.Name, help[series.Name], nil, series.Labels)
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, series.Value)
	}
}

func (e *Exporter) snapshot(ctx context.Context) ([]Card, error) {
	var pools v1alpha1.GPUPoolList
	if err := e.Reader.List(ctx, &pools); err != nil {
		return nil, err
	}

	var modelList v1alpha1.GPUModelList
	if err := e.Reader.List(ctx, &modelList); err != nil {
		return nil, err
	}
	models := make(map[string]*v1alpha1.GPUModel, len(modelList.Items))
	for i := range modelList.Items {
		models[modelList.Items[i].Name] = &modelList.Items[i]
	}

	var published resourcev1.ResourceSliceList
	if err := e.Reader.List(ctx, &published); err != nil {
		return nil, err
	}

	var claims resourcev1.ResourceClaimList
	if err := e.Reader.List(ctx, &claims); err != nil {
		return nil, err
	}

	// Pods are read for their labels alone, so a utilization profile can be
	// matched against the job holding a GPU. A failure here is not fatal: the
	// fleet is still describable without per-workload overrides, and losing
	// them is better than losing every metric.
	workloads := Workloads{}
	var pods corev1.PodList
	if err := e.Reader.List(ctx, &pods); err == nil {
		for i := range pods.Items {
			pod := &pods.Items[i]
			workloads[Holder{Namespace: pod.Namespace, Pod: pod.Name}] = pod.Labels
		}
	}

	return CardsFrom(pools.Items, models, published.Items, Holders(claims.Items), workloads), nil
}

// Holders maps each allocated ghostgpu device to the workload holding it.
//
// This is the attribution real dcgm-exporter deployments struggle with, and it
// is nearly free here: the scheduler already recorded exact device identity in
// ResourceClaim.status, so there is nothing to re-derive from a container
// runtime and nothing to get wrong under MIG.
//
// Claims for other drivers are skipped, so a cluster running a real GPU driver
// alongside ghostgpu never shows phantom holders on simulated devices.
func Holders(claims []resourcev1.ResourceClaim) map[Allocation]Holder {
	held := map[Allocation]Holder{}

	for i := range claims {
		claim := &claims[i]
		if claim.Status.Allocation == nil {
			continue
		}

		var holder Holder
		for _, consumer := range claim.Status.ReservedFor {
			if consumer.Resource == podsResource {
				holder = Holder{Namespace: claim.Namespace, Pod: consumer.Name}
				break
			}
		}
		if holder.Pod == "" {
			// Allocated but not yet reserved by a pod. There is genuinely no
			// workload to attribute it to, and naming one would be a guess.
			continue
		}

		for _, result := range claim.Status.Allocation.Devices.Results {
			if result.Driver != gpu.DriverName {
				continue
			}
			held[Allocation{Pool: result.Pool, Device: result.Device}] = holder
		}
	}
	return held
}
