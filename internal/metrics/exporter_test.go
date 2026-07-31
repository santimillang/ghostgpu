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
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/santimillang/ghostgpu/api/v1alpha1"
)

func exporterScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme, resourcev1.AddToScheme, v1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}
	return s
}

// gather renders what Prometheus would actually scrape, so the assertions are
// against the exposition rather than an internal structure.
//
// Built by walking the gathered families rather than by reading a diff out of a
// comparison helper: a test that asserts against an error message passes just
// as happily when there is no error and nothing was collected.
func gather(t *testing.T, objects ...runtime.Object) string {
	t.Helper()

	c := fake.NewClientBuilder().WithScheme(exporterScheme(t)).WithRuntimeObjects(objects...).Build()

	registry := prometheus.NewRegistry()
	if err := registry.Register(&Exporter{Reader: c}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	if len(families) == 0 {
		t.Fatal("nothing was collected, so any assertion below would be vacuous")
	}

	var buf strings.Builder
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			buf.WriteString(family.GetName() + "{")
			for _, label := range metric.GetLabel() {
				fmt.Fprintf(&buf, "%s=%q,", label.GetName(), label.GetValue())
			}
			fmt.Fprintf(&buf, "} %v\n", metric.GetGauge().GetValue())
		}
	}
	return buf.String()
}

// The end-to-end shape: objects in a cluster become scrapeable metrics with the
// names and labels real tooling queries.
func TestExporterRendersScrapeableMetrics(t *testing.T) {
	pool := testPools(&v1alpha1.UtilizationSpec{
		WhenAllocated: &v1alpha1.UtilizationSample{
			GPUUtil:    ptr.To(int32(85)),
			PowerWatts: ptr.To(int32(550)),
		},
	})[0]

	models := testModels()
	published := slice(wholeGPU(0), wholeGPU(1))

	claim := resourcev1.ResourceClaim{}
	claim.Name, claim.Namespace = "trainer-claim", testNamespace
	claim.Status.ReservedFor = []resourcev1.ResourceClaimConsumerReference{
		{Resource: podsResource, Name: testWorkload},
	}
	claim.Status.Allocation = &resourcev1.AllocationResult{
		Devices: resourcev1.DeviceAllocationResult{
			Results: []resourcev1.DeviceRequestAllocationResult{
				{Driver: "gpu.ghostgpu.dev", Pool: testNode, Device: firstDevice},
			},
		},
	}

	out := gather(t, &pool, models[testModel], &published, &claim)

	for _, want := range []string{
		`DCGM_FI_DEV_GPU_UTIL`,
		`Hostname="node-a"`,
		`pod="trainer"`,
		`namespace="team-a"`,
		`modelName="NVIDIA H100 80GB HBM3"`,
		`DCGM_FI_DEV_POWER_USAGE`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition is missing %q:\n%s", want, out)
		}
	}
}

// A read failure must yield no samples rather than stale or invented ones.
// Prometheus recording a failed scrape is truthful; a simulator reporting
// numbers for a cluster it could not read is the one failure mode to avoid.
func TestExporterEmitsNothingWhenTheClusterCannotBeRead(t *testing.T) {
	// A scheme without the ghostgpu types makes every List fail.
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	count := testutil.CollectAndCount(&Exporter{Reader: c})
	if count != 0 {
		t.Errorf("collected %d samples from an unreadable cluster, want 0", count)
	}
}

func TestExporterCollectsNothingForAnEmptyCluster(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(exporterScheme(t)).Build()

	if count := testutil.CollectAndCount(&Exporter{Reader: c}); count != 0 {
		t.Errorf("collected %d samples with no pools, want 0", count)
	}
}
