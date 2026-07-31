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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/santimillang/ghostgpu/test/utils"
)

const (
	faultsPool  = "faults-pool"
	faultsNode  = "ghost-faults-0"
	faultsClass = "ghostgpu-faults-e2e"
)

// Hardware failure is the hardest scenario to test for, because it cannot be
// arranged on demand: you cannot ask a production GPU to fall off the bus to
// see whether your remediation drains the node.
//
// The assertion that matters is not that a failed GPU stops accepting work —
// occupancy already does that — but that a job *already running* on it is
// thrown off and its claim released, so it can be rescheduled onto healthy
// hardware.
var _ = Describe("faults", Ordered, func() {
	BeforeAll(func() {
		By("waiting for the controller-manager to be available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())

		By("creating a healthy two-GPU fleet")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", testdata("faults.yaml")))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", faultsPool, "{.status.devicesPublished}")).To(Equal("2"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		for _, pod := range []string{"fault-victim", "fault-survivor"} {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", pod, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "resourceclaim", pod+"-claim", "--ignore-not-found"))
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
			testdata("faults.yaml"), "--ignore-not-found"))
	})

	// The control. Without a job genuinely running on healthy hardware first,
	// its later absence would prove nothing: a pod that never scheduled is also
	// a pod that is not there.
	It("places a workload on the healthy fleet", func() {
		Expect(applyYAML(gpuClaim("fault-victim", 1, faultsClass))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(podPhase(g, "fault-victim")).To(Equal("Running"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		Expect(kubectlOut("get", "resourceclaim", "fault-victim-claim",
			"-o", "jsonpath={.status.allocation.devices.results[0].device}")).NotTo(BeEmpty())
	})

	// The assertion the feature exists for.
	It("evicts the running workload when its GPU fails", func() {
		By("declaring both GPUs failed with XID 79")
		_, err := utils.Run(exec.Command("kubectl", "patch", "gpupool", faultsPool,
			"--type=merge", "-p",
			`{"spec":{"faults":[{"gpus":2,"effect":"Evict","xid":79}]}}`))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			_, err := kubectlTry("get", "pod", "fault-victim")
			g.Expect(err).To(HaveOccurred(),
				"the workload is still running on a GPU that has failed")
		}, 2*time.Minute, 3*time.Second).Should(Succeed())

		By("reporting the failure on the pool")
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "gpupool", faultsPool, "{.status.gpusFaulted}")).To(Equal("2"))
		}, time.Minute, 2*time.Second).Should(Succeed())
	})

	// A failed GPU must also be out of the allocatable pool, or a replacement
	// job would land straight back on the broken hardware.
	It("refuses new work on the failed hardware", func() {
		Eventually(func(g Gomega) {
			g.Expect(jsonPath(g, "node", faultsNode, `{.status.capacity.nvidia\.com/gpu}`)).To(Equal("2"))
			g.Expect(jsonPath(g, "node", faultsNode, `{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("0"))
		}, time.Minute, 2*time.Second).Should(Succeed())

		Expect(applyYAML(gpuClaim("fault-survivor", 1, faultsClass))).To(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(podPhase(g, "fault-survivor")).To(Equal("Pending"))
		}, 20*time.Second, 5*time.Second).Should(Succeed())
	})

	// The negative control for the whole suite: repairing the hardware has to
	// make the pending job schedulable. Without this, every Pending above could
	// have been caused by something other than the fault.
	It("places that job once the hardware is repaired", func() {
		_, err := utils.Run(exec.Command("kubectl", "patch", "gpupool", faultsPool,
			"--type=json", `-p=[{"op":"remove","path":"/spec/faults"}]`))
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			g.Expect(podPhase(g, "fault-survivor")).To(Equal("Running"))
		}, 2*time.Minute, 3*time.Second).Should(Succeed(),
			"repairing the GPUs did not make the pending job schedulable, so its "+
				"Pending was caused by something other than the fault")
	})
})
