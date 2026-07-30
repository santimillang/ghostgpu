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

// Command ghostgpu is the ghostgpu command-line interface.
//
// Adoption should not depend on hand-writing custom resources, so `ghostgpu up`
// turns a handful of flags into the GPUModel/GPUPool pair and applies it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/santimillang/ghostgpu/internal/cli"
)

const usage = `ghostgpu simulates GPU clusters on Kubernetes.

Usage:
  ghostgpu up [flags]     create or update a simulated GPU pool

Run "ghostgpu up -h" for the available flags.
`

// say writes a line to w. Failures writing to stdout are not actionable in a
// CLI, so the error is explicitly discarded rather than silently unchecked.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		say(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "up" {
		say(stderr, "%s", usage)
		os.Exit(2)
	}

	fs := flag.NewFlagSet("ghostgpu up", flag.ExitOnError)
	fs.SetOutput(stderr)

	var (
		opts        cli.UpOptions
		gpus        int
		nvlink      int
		dryRun      bool
		waitTimeout time.Duration
	)
	fs.StringVar(&opts.Name, "name", "h100", "name for the GPUModel; the pool is named <name>-pool")
	fs.StringVar(&opts.Product, "gpu", "NVIDIA-H100-SXM",
		"GPU product name, as GPU Feature Discovery would report it")
	fs.StringVar(&opts.Memory, "memory", "80Gi", "memory per simulated GPU")
	fs.StringVar(&opts.Compute, "compute-capability", "9.0", "compute capability, as <major>.<minor>")
	fs.IntVar(&gpus, "gpus-per-node", 8, "simulated GPUs per node (1-128)")
	fs.IntVar(&nvlink, "nvlink-domain-size", 4, "GPUs per NVLink domain (0 disables domain attributes)")
	fs.BoolVar(&opts.NUMAAware, "numa", false, "emit a NUMA node attribute per simulated device")
	fs.StringVar(&opts.NodeSelector, "node-selector", "type=kwok",
		"labels selecting which nodes receive GPUs; empty matches every simulated node")
	fs.StringVar(&opts.SharingMode, "sharing-mode", "none",
		"how each GPU is divided: none, or mig to partition it into MIG instances")
	fs.StringVar(&opts.MIGProfiles, "mig-profiles", "",
		"restrict a MIG pool to these profiles, e.g. 1g.10gb,3g.40gb; empty uses every profile the GPU supports")
	fs.BoolVar(&opts.DRA, "dra", true, "publish DRA ResourceSlices")
	fs.BoolVar(&opts.ExtendedResource, "extended-resource", true, "advertise nvidia.com/gpu node capacity")
	fs.BoolVar(&dryRun, "dry-run", false, "print the manifests as YAML instead of applying them")
	fs.DurationVar(&waitTimeout, "wait", 60*time.Second,
		"how long to wait for the operator to publish devices (0 skips waiting)")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	opts.GPUsPerNode = int32(gpus)
	opts.NVLinkDomainSize = int32(nvlink)

	objs, err := cli.BuildManifests(opts)
	if err != nil {
		return err
	}

	// Rendering never contacts a cluster, so --dry-run works with no kubeconfig
	// at all and its output pipes straight into kubectl apply -f -.
	if dryRun {
		rendered, err := cli.RenderYAML(objs)
		if err != nil {
			return err
		}
		say(stdout, "%s", rendered)
		return nil
	}

	c, err := newClient()
	if err != nil {
		return err
	}

	ctx := context.Background()
	results, err := cli.Apply(ctx, c, objs)
	// Print whatever succeeded before reporting the failure, so a partial apply
	// is visible rather than silently discarded.
	for _, r := range results {
		say(stdout, "%s\n", r)
	}
	if err != nil {
		return err
	}

	if waitTimeout > 0 {
		reportPool(ctx, c, opts.Name+"-pool", waitTimeout, stdout)
	}
	return nil
}

func newClient() (client.Client, error) {
	scheme, err := cli.Scheme()
	if err != nil {
		return nil, err
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("no kubeconfig found: %w", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster: %w", err)
	}
	return c, nil
}

// reportPool waits for the operator to publish devices and says what happened.
// A timeout here is informative, not fatal: the resources were applied, and the
// most common cause is simply that no simulated node matches the selector yet.
func reportPool(ctx context.Context, c client.Client, name string, timeout time.Duration, stdout io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	report, err := cli.WaitForPool(ctx, c, name, time.Second)
	switch {
	case err == nil:
		say(stdout, "simulating %d GPUs across %d nodes\n",
			report.DevicesPublished, report.NodesMatched)
	case errors.Is(err, context.DeadlineExceeded):
		say(stdout,
			"applied, but no devices published after %s.\n"+
				"Is the ghostgpu operator running, and are there kwok nodes matching the selector?\n"+
				"kwok nodes need the kwok.x-k8s.io/node annotation; ghostgpu never touches nodes without it.\n",
			timeout)
	default:
		say(stdout, "applied, but reading pool status failed: %v\n", err)
	}
}
