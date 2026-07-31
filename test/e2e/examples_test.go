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
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/santimillang/ghostgpu/test/utils"
)

func example(parts ...string) string {
	return filepath.Join(append([]string{"examples"}, parts...)...)
}

// The scenarios in examples/ are the first thing anyone runs, and a quickstart
// that has quietly rotted is worse than none at all: it costs a newcomer their
// first half hour and their confidence in everything else.
//
// So they are applied for real here and checked against the outcome their
// README claims. What is asserted is deliberately the headline of each
// scenario, not its incidental details — the point is that the documented story
// still happens, not that every number is pinned twice.
var _ = Describe("examples", Ordered, func() {
	BeforeAll(func() {
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
				"deployment/ghostgpu-controller-manager", "-n", namespace, "--timeout=30s"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}, 3*time.Minute, 5*time.Second).Should(Succeed())
	})

	Context("fragmented-fleet", func() {
		BeforeAll(func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", example("fragmented-fleet")))
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("fragmented-fleet", "jobs"), "--ignore-not-found"))
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("fragmented-fleet"), "--ignore-not-found"))
			})
		})

		// The README's status table: 16 devices, 9 occupied, 7 free.
		It("comes up 2/2/2/1 free as documented", func() {
			Eventually(func(g Gomega) {
				g.Expect(jsonPath(g, "gpupool", "fragmented", "{.status.nodesMatched}")).To(Equal("4"))
				g.Expect(jsonPath(g, "gpupool", "fragmented", "{.status.devicesPublished}")).To(Equal("16"))
				g.Expect(jsonPath(g, "gpupool", "fragmented", "{.status.gpusOccupied}")).To(Equal("9"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())
		})

		// The claim the scenario exists to demonstrate.
		It("refuses the four-GPU job while seven GPUs are free", func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f",
				example("fragmented-fleet", "jobs", "four-gpu.yaml")))
			Expect(err).NotTo(HaveOccurred())

			Consistently(func(g Gomega) {
				g.Expect(podPhase(g, "frag-4gpu")).To(Equal("Pending"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})
	})

	Context("gpu-failure", func() {
		BeforeAll(func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", example("gpu-failure")))
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("gpu-failure", "jobs"), "--ignore-not-found"))
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("gpu-failure"), "--ignore-not-found"))
			})
		})

		It("evicts the running job when its node's GPUs fail", func() {
			By("placing the trainer on healthy hardware")
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f",
				example("gpu-failure", "jobs", "trainer.yaml")))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "trainer")).To(Equal("Running"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("running the README's patch verbatim")
			_, err = utils.Run(exec.Command("kubectl", "patch", "gpupool", "failure-demo",
				"--type=merge", "-p", `{"spec":{"faults":[{`+
					`"nodeSelector": {"kubernetes.io/hostname": "ghost-fail-0"},`+
					`"gpus": 2, "effect": "Evict", "xid": 79}]}}`))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				_, err := kubectlTry("get", "pod", "trainer")
				g.Expect(err).To(HaveOccurred(), "the job survived its GPU failing")
			}, 2*time.Minute, 3*time.Second).Should(Succeed())
		})

		// The repair half, which is where the recovery bugs live.
		It("returns the hardware to service when the fault is removed", func() {
			_, err := utils.Run(exec.Command("kubectl", "patch", "gpupool", "failure-demo",
				"--type=json", `-p=[{"op":"remove","path":"/spec/faults"}]`))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				// Allocatable is the assertion that matters: the hardware is
				// back in service and can take work again.
				g.Expect(jsonPath(g, "node", "ghost-fail-0",
					`{.status.allocatable.nvidia\.com/gpu}`)).To(Equal("2"))

				// gpusFaulted is omitempty, so a repaired pool reports it as
				// absent rather than as "0". Both readings mean no faults.
				g.Expect(jsonPath(g, "gpupool", "failure-demo",
					"{.status.gpusFaulted}")).To(BeElementOf("", "0"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())
		})
	})

	Context("mig-exclusivity", func() {
		BeforeAll(func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", example("mig-exclusivity")))
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("mig-exclusivity", "jobs"), "--ignore-not-found"))
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("mig-exclusivity"), "--ignore-not-found"))
			})
		})

		It("publishes six profiles from one card, and can satisfy only one", func() {
			Eventually(func(g Gomega) {
				g.Expect(jsonPath(g, "gpupool", "mig-demo", "{.status.devicesPublished}")).To(Equal("6"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("taking the whole card")
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f",
				example("mig-exclusivity", "jobs", "whole-card.yaml")))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "whole-card")).To(Equal("Running"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			By("failing to take a slice of the same silicon")
			_, err = utils.Run(exec.Command("kubectl", "apply", "-f",
				example("mig-exclusivity", "jobs", "one-slice.yaml")))
			Expect(err).NotTo(HaveOccurred())
			Consistently(func(g Gomega) {
				g.Expect(podPhase(g, "one-slice")).To(Equal("Pending"))
			}, 30*time.Second, 5*time.Second).Should(Succeed())

			// The README's own closing check, and the control for the above:
			// releasing the card has to make the slice schedulable.
			By("releasing the card")
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", "whole-card", "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "resourceclaim",
				"whole-card-claim", "--ignore-not-found"))

			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "one-slice")).To(Equal("Running"))
			}, 2*time.Minute, 3*time.Second).Should(Succeed())
		})
	})

	Context("idle-reclamation", func() {
		BeforeAll(func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f", example("idle-reclamation")))
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("idle-reclamation", "jobs"), "--ignore-not-found"))
				_, _ = utils.Run(exec.Command("kubectl", "delete", "-f",
					example("idle-reclamation"), "--ignore-not-found"))
			})
		})

		It("reports the two workloads at the documented utilizations", func() {
			_, err := utils.Run(exec.Command("kubectl", "apply", "-f",
				example("idle-reclamation", "jobs")))
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, "trainer")).To(Equal("Running"))
				g.Expect(podPhase(g, "notebook")).To(Equal("Running"))
			}, 2*time.Minute, 2*time.Second).Should(Succeed())

			// The README shows 92 for the trainer and 3 for the notebook.
			Eventually(func(g Gomega) {
				exposition := scrape(g)
				g.Expect(utilizationFor(exposition, "trainer")).To(Equal("92"))
				g.Expect(utilizationFor(exposition, "notebook")).To(Equal("3"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})
})
