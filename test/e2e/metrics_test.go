//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/santimillang/ghostgpu/test/utils"
)

const (
	metricsPool = "metrics-pool"
	metricsNode = "ghost-metrics-0"
)

// scrape reads the simulated fleet's metrics the way Prometheus would, from
// inside the cluster.
//
// A throwaway pod rather than kubectl port-forward: a port-forward is a
// long-lived background process that has to be cleaned up and raced against,
// whereas this either returns the exposition or fails.
//
// Deliberately NOT `kubectl run -i --rm`. That attaches to the pod, and for a
// container which exits almost immediately kubectl can emit the stream twice —
// returning the whole exposition duplicated, so every device was counted twice
// and an assertion expecting two idle GPUs intermittently saw four. Waiting for
// the pod to finish and reading its log returns the curl output exactly once.
func scrape(g Gomega) string {
	remove := func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", "metrics-scrape",
			"--ignore-not-found", "--wait=true"))
	}
	remove()
	defer remove()

	out, err := utils.Run(exec.Command("kubectl", "run", "metrics-scrape",
		"--restart=Never", "--image=curlimages/curl:8.11.1",
		"--command", "--",
		"curl", "-s", fmt.Sprintf("http://ghostgpu-gpu-metrics.%s.svc:9400/metrics", namespace)))
	g.Expect(err).NotTo(HaveOccurred(), out)

	out, err = utils.Run(exec.Command("kubectl", "wait", "pod/metrics-scrape",
		"--for=jsonpath={.status.phase}=Succeeded", "--timeout=60s"))
	g.Expect(err).NotTo(HaveOccurred(), out)

	out, err = utils.Run(exec.Command("kubectl", "logs", "metrics-scrape"))
	g.Expect(err).NotTo(HaveOccurred(), out)
	return out
}

// utilizationFor pulls one pod's reported GPU utilization out of the
// exposition, or "" when no series is attributed to it.
func utilizationFor(exposition, pod string) string {
	for _, line := range strings.Split(exposition, "\n") {
		if strings.HasPrefix(line, "DCGM_FI_DEV_GPU_UTIL") && strings.Contains(line, `pod="`+pod+`"`) {
			fields := strings.Fields(line)
			return fields[len(fields)-1]
		}
	}
	return ""
}

// The claim these metrics rest on is not that ghostgpu has an endpoint — other
// simulators do — but that the numbers are attributed correctly. That comes
// from ResourceClaim.status, which the scheduler owns, so only a real scheduler
// can show it working.
var _ = Describe("metrics", Ordered, func() {
	var exposition string

	BeforeAll(func() {
		By("waiting for the controller-manager to be available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("creating a pool that declares what a busy GPU reports")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("metrics.yaml")))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", metricsPool, "{.status.devicesPublished}")).To(Equal("4"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("placing two workloads on it: one well-behaved, one wasteful")
		for _, name := range []string{"metrics-trainer", "wasteful"} {
			Expect(applyYAML(gpuClaim(name, 1, "ghostgpu-metrics-e2e"))).To(Succeed())
		}
		Eventually(func(g Gomega) {
			g.Expect(podPhase(g, "metrics-trainer")).To(Equal("Running"))
			g.Expect(podPhase(g, "wasteful")).To(Equal("Running"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		// Wait until both workloads actually appear attributed, not merely
		// until the metric name exists. A pod reaching Running and the exporter
		// observing its allocation are different moments, and the first scrape
		// satisfies a name check while still describing an unheld fleet — which
		// would make every assertion below race the exporter.
		Eventually(func(g Gomega) {
			exposition = scrape(g)
			g.Expect(exposition).To(ContainSubstring("DCGM_FI_DEV_GPU_UTIL"))
			g.Expect(utilizationFor(exposition, "metrics-trainer")).NotTo(BeEmpty(),
				"the exporter has not yet attributed a GPU to the trainer")
			g.Expect(utilizationFor(exposition, "wasteful")).NotTo(BeEmpty(),
				"the exporter has not yet attributed a GPU to the wasteful job")
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		for _, pod := range []string{"metrics-trainer", "wasteful"} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", pod, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "resourceclaim",
				pod+"-claim", "--ignore-not-found"))
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
			testdata("metrics.yaml"), "--ignore-not-found"))
	})

	It("serves the metric names real tooling queries", func() {
		for _, name := range []string{
			"DCGM_FI_DEV_GPU_UTIL",
			"DCGM_FI_DEV_FB_USED",
			"DCGM_FI_DEV_FB_FREE",
			"DCGM_FI_PROF_GR_ENGINE_ACTIVE",
			"DCGM_FI_DEV_XID_ERRORS",
		} {
			Expect(exposition).To(ContainSubstring(name))
		}
	})

	// The differentiator. Real dcgm-exporter deployments have a long tail of
	// bugs attributing a GPU to the workload holding it; ghostgpu reads it out
	// of the allocation the scheduler already wrote.
	It("attributes a busy GPU to the pod the scheduler gave it to", func() {
		var busy string
		for _, line := range strings.Split(exposition, "\n") {
			if strings.HasPrefix(line, "DCGM_FI_DEV_GPU_UTIL") && strings.Contains(line, `pod="metrics-trainer"`) {
				busy = line
				break
			}
		}
		Expect(busy).NotTo(BeEmpty(),
			"no GPU utilization series is attributed to the pod holding a GPU:\n"+exposition)

		// The declared busy reading, not a default and not a random number.
		Expect(busy).To(HaveSuffix(" 85"))
		Expect(busy).To(ContainSubstring(`namespace="default"`))
		Expect(busy).To(ContainSubstring(`modelName="NVIDIA-H100-80GB-HBM3"`))
		Expect(busy).To(MatchRegexp(`Hostname="ghost-metrics-\d"`))
	})

	// An empty pod label is a distinct time series that sum by (pod) would
	// group on, which is exactly the confusion to avoid.
	It("gives idle GPUs no workload labels at all", func() {
		// Scoped to this scenario's own node, because the exporter publishes the
		// whole cluster from one endpoint.
		//
		// Deliberately does NOT assert how many devices are idle. A DRA claim is
		// satisfied by any device of the right class anywhere in the cluster, so
		// a workload from another suite can legitimately hold one of this node's
		// GPUs and move that count — which is what made an earlier Equal(2) here
		// flake. What this test is about is what an unheld device reports, not
		// how many happen to be unheld.
		var onNode, idle []string
		for _, line := range strings.Split(exposition, "\n") {
			if !strings.HasPrefix(line, "DCGM_FI_DEV_GPU_UTIL") ||
				!strings.Contains(line, `Hostname="`+metricsNode+`"`) {
				continue
			}
			onNode = append(onNode, line)
			if !strings.Contains(line, "pod=") {
				idle = append(idle, line)
			}
		}

		Expect(onNode).To(HaveLen(4),
			"each of the node's four GPUs should report utilization; its lines were:\n"+
				strings.Join(onNode, "\n"))

		for _, line := range idle {
			Expect(line).NotTo(ContainSubstring("namespace="),
				"an idle GPU carries no workload labels at all rather than empty ones: "+line)
			Expect(line).To(HaveSuffix(" 0"), "an unheld GPU is not idle: "+line)
		}

		// Guards the loop above against passing vacuously. If every device were
		// held there would be nothing to check, and if none were held the fleet
		// would not be exercising attribution at all.
		Expect(idle).NotTo(BeEmpty(), "no idle GPU left on the node to assert against:\n"+
			strings.Join(onNode, "\n"))
		Expect(utilizationFor(exposition, "metrics-trainer")).NotTo(BeEmpty(),
			"this suite's own workload should be holding one of the node's GPUs")
		Expect(utilizationFor(exposition, "wasteful")).NotTo(BeEmpty(),
			"this suite's own workload should be holding one of the node's GPUs")
	})

	// The fixture idle-GPU reclamation needs, and the reason per-workload
	// profiles exist: two jobs on one fleet, one using its GPU properly and one
	// wasting it. A fleet where every allocated card reports the same number
	// cannot ask the question those tools answer.
	It("reports different utilization for different workloads", func() {
		Expect(utilizationFor(exposition, "metrics-trainer")).To(Equal("85"),
			"the pool default should apply")
		Expect(utilizationFor(exposition, "wasteful")).To(Equal("4"),
			"the matching workload profile should override it")

		// The wasteful job is holding most of the framebuffer while doing
		// nothing, which combined with low utilization is the signal real idle
		// detection keys on.
		for _, line := range strings.Split(exposition, "\n") {
			if strings.HasPrefix(line, "DCGM_FI_DEV_FB_USED") && strings.Contains(line, `pod="wasteful"`) {
				fields := strings.Fields(line)
				Expect(fields[len(fields)-1]).To(Equal("77824"), "95% of 80Gi in MiB")
			}
		}
	})

	// ghostgpu has no thermal model, so a wattage nobody declared would be
	// fabrication rather than simulation.
	It("omits power and temperature, which this pool never declared", func() {
		// Scoped to this node for the same reason as above: another scenario
		// declaring a wattage must not make this look like a regression.
		for _, line := range strings.Split(exposition, "\n") {
			if !strings.Contains(line, `Hostname="`+metricsNode+`"`) {
				continue
			}
			Expect(line).NotTo(HavePrefix("DCGM_FI_DEV_POWER_USAGE"))
			Expect(line).NotTo(HavePrefix("DCGM_FI_DEV_GPU_TEMP"))
		}
	})
})
