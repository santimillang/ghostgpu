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
// turns a handful of flags into the GPUModel/GPUPool pair and applies it, and
// `ghostgpu capture` derives that pair from a cluster that already exists.
//
// Only `up` writes anything. `status` and `capture` read and print.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ghostgpuv1alpha1 "github.com/santimillang/ghostgpu/api/v1alpha1"
	"github.com/santimillang/ghostgpu/internal/cli"
)

const usage = `ghostgpu simulates GPU clusters on Kubernetes.

Usage:
  ghostgpu up [flags]       create or update a simulated GPU pool
  ghostgpu status [flags]   show what is published and who holds it
  ghostgpu capture [flags]  read a real cluster's GPU fleet and print manifests reproducing it
  ghostgpu version          print the version of this binary

Run "ghostgpu <command> -h" for the available flags.
`

// Populated by ldflags at release time. GoReleaser writes these exact symbols
// by default, so the release configuration needs no ldflags of its own.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

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
	if len(args) == 0 {
		say(stderr, "%s", usage)
		os.Exit(2)
	}

	switch args[0] {
	case "up":
		return runUp(args, stdout, stderr)
	case "status":
		return runStatus(args, stdout, stderr)
	case "capture":
		return runCapture(args, stdout, stderr)
	case "version", "--version":
		say(stdout, "%s\n", cli.VersionLine(cli.BuildInfo{
			Version: version,
			Commit:  commit,
			Date:    date,
		}))
		return nil
	default:
		say(stderr, "%s", usage)
		os.Exit(2)
		return nil
	}
}

// runCapture reproduces a real cluster's GPU fleet as ghostgpu manifests.
//
// It never writes anywhere. Not to the source cluster — the client it holds is
// a cli.ReadOnly, which has no write method to call — and not to the local one
// either: the manifests go to stdout, so applying them stays the user's own
// explicit act. Pointing a simulator at production has to be provably harmless,
// and "prints YAML" is the easiest thing to prove.
//
// Warnings go to stderr so that `ghostgpu capture > fleet.yaml` still produces
// a clean file while the user sees what could not be reproduced.
func runCapture(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ghostgpu capture", flag.ExitOnError)
	fs.SetOutput(stderr)

	kubeContext := fs.String("context", "",
		"kubeconfig context of the cluster to read; empty uses the current one")
	emitNodes := fs.Bool("nodes", true,
		"also emit the kwok Node manifests reproducing the fleet, so applying the output is enough")
	gpusPerNode := fs.Int("gpus-per-node", 0,
		"physical GPUs per node, for clusters that publish nothing revealing it; 0 derives it")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	reader, err := newReader(*kubeContext)
	if err != nil {
		return err
	}
	ctx := context.Background()

	var nodes corev1.NodeList
	if err := reader.List(ctx, &nodes); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	// ResourceSlices are optional. A cluster with no DRA has none, and a
	// read-only kubeconfig may not be allowed to see them; neither is a reason
	// to refuse a capture that node labels alone can mostly satisfy.
	var published resourcev1.ResourceSliceList
	if err := reader.List(ctx, &published); err != nil {
		say(stderr, "warning: could not read ResourceSlices (%v); capturing without DRA topology\n", err)
	}

	result, err := cli.Capture(nodes.Items, published.Items, cli.CaptureOptions{
		GPUsPerNode: int32(*gpusPerNode),
		EmitNodes:   *emitNodes,
	})
	if err != nil {
		return err
	}

	for _, warning := range result.Warnings {
		say(stderr, "warning: %s\n", warning)
	}

	rendered, err := cli.RenderYAML(result.Objects)
	if err != nil {
		return err
	}
	say(stdout, "%s", rendered)
	return nil
}

// runStatus answers "what is published, and who holds it".
//
// Everything is derived from objects already in the cluster, so this reads and
// never writes — the same information a user would otherwise assemble from
// several kubectl jsonpath queries.
func runStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ghostgpu status", flag.ExitOnError)
	fs.SetOutput(stderr)

	node := fs.String("node", "", "show devices on this node only")
	devices := fs.Bool("devices", false, "list individual devices and their holders")
	budgets := fs.Bool("budgets", false,
		"show each GPU's consumed compute slices and memory, which is what explains why a MIG instance will not fit")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	c, err := newClient()
	if err != nil {
		return err
	}
	ctx := context.Background()

	var pools ghostgpuv1alpha1.GPUPoolList
	if err := c.List(ctx, &pools); err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}

	var slices resourcev1.ResourceSliceList
	if err := c.List(ctx, &slices); err != nil {
		return fmt.Errorf("listing resource slices: %w", err)
	}

	var claims resourcev1.ResourceClaimList
	if err := c.List(ctx, &claims); err != nil {
		return fmt.Errorf("listing resource claims: %w", err)
	}

	report := cli.BuildReport(pools.Items, slices.Items, claims.Items)

	// --node filters a view rather than choosing one, so that --budgets --node
	// shows budgets for that node. It only implies the device list when no view
	// was asked for, since the pool summary does not change when a node is named.
	showDevices := *devices
	if !*devices && !*budgets {
		if *node == "" {
			say(stdout, "%s", cli.RenderPools(report))
			if len(report.Pools) == 0 {
				say(stdout, "no GPUPools found\n")
			}
			return nil
		}
		showDevices = true
	}

	if showDevices {
		say(stdout, "%s", cli.RenderDevices(report, *node))
	}
	if *budgets {
		if showDevices {
			say(stdout, "\n")
		}
		say(stdout, "%s", cli.RenderGPUs(report, *node))
	}
	return nil
}

func runUp(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ghostgpu up", flag.ExitOnError)
	fs.SetOutput(stderr)

	var (
		opts        cli.UpOptions
		gpus        int
		nvlink      int
		busy        int
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
	fs.StringVar(&opts.MIGPartition, "mig-partition", "",
		"declare which MIG instances exist per GPU, e.g. 3g.40gb=1,1g.10gb=4; "+
			"empty advertises every profile as a possibility instead")
	fs.IntVar(&busy, "busy-per-node", 0,
		"GPUs per node to mark already occupied, so the fleet starts partly full; "+
			"uneven occupancy needs spec.occupancy in YAML")
	fs.BoolVar(&opts.DRA, "dra", true, "publish DRA ResourceSlices")
	fs.BoolVar(&opts.ExtendedResource, "extended-resource", true, "advertise nvidia.com/gpu node capacity")
	fs.BoolVar(&dryRun, "dry-run", false, "print the manifests as YAML instead of applying them")
	fs.DurationVar(&waitTimeout, "wait", 60*time.Second,
		"how long to wait for the operator to publish devices (0 skips waiting)")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	// flag.Int parses into a 64-bit int while the API stores these as int32, so
	// narrowing silently wraps: --gpus-per-node 4294967304 became 8 and applied
	// a fleet nobody asked for. Range-check before narrowing, so the number the
	// user typed is the number named in the error.
	for _, f := range []struct {
		name  string
		value int
		into  *int32
	}{
		{"gpus-per-node", gpus, &opts.GPUsPerNode},
		{"nvlink-domain-size", nvlink, &opts.NVLinkDomainSize},
		{"busy-per-node", busy, &opts.BusyPerNode},
	} {
		if f.value > math.MaxInt32 || f.value < math.MinInt32 {
			return fmt.Errorf("--%s is out of range: %d", f.name, f.value)
		}
		*f.into = int32(f.value)
	}

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

// newReader connects to a cluster with the writes taken away.
//
// The concrete client is narrowed to cli.ReadOnly before it is ever returned,
// so no caller can widen it back — capture cannot write to the cluster it was
// pointed at even by mistake.
func newReader(kubeContext string) (cli.ReadOnly, error) {
	scheme, err := cli.Scheme()
	if err != nil {
		return cli.ReadOnly{}, err
	}

	cfg, err := configFor(kubeContext)
	if err != nil {
		return cli.ReadOnly{}, err
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return cli.ReadOnly{}, fmt.Errorf("connecting to the cluster: %w", err)
	}
	return cli.NewReadOnly(c), nil
}

// configFor resolves a named kubeconfig context, or the current one when the
// name is empty.
//
// Naming the context explicitly is what makes capture safe to run against a
// cluster that is not the one you are simulating into: pointing at production
// should not mean switching your current context to it first.
func configFor(kubeContext string) (*rest.Config, error) {
	if kubeContext == "" {
		cfg, err := ctrl.GetConfig()
		if err != nil {
			return nil, fmt.Errorf("no kubeconfig found: %w", err)
		}
		return cfg, nil
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig context %q: %w", kubeContext, err)
	}
	return cfg, nil
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
