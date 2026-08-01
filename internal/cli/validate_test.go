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

package cli_test

import (
	"strings"
	"testing"

	"github.com/santimillang/ghostgpu/internal/cli"
)

// valid returns options that BuildManifests accepts, so each test below can
// change exactly one field and attribute the failure to it.
func valid() cli.UpOptions {
	return cli.UpOptions{
		Name:        "h100",
		Product:     "NVIDIA-H100-80GB-HBM3",
		Memory:      "80Gi",
		Compute:     "9.0",
		GPUsPerNode: 8,
	}
}

// productName is written verbatim into the nvidia.com/gpu.product node label
// (internal/gpu/nodelabels.go). A value the API server will not accept as a
// label makes every node patch fail, and the only trace is in operator logs —
// the pool reports no devices and does not say why.
func TestBuildManifestsRejectsProductNameThatCannotBeALabelValue(t *testing.T) {
	for _, product := range []string{
		"NVIDIA H100 80GB", // spaces
		"NVIDIA/H100",      // slash
		strings.Repeat("a", 64),
		"-leading-dash",
	} {
		opts := valid()
		opts.Product = product

		if _, err := cli.BuildManifests(opts); err == nil {
			t.Errorf("BuildManifests accepted product %q, which cannot be a node label value", product)
		}
	}
}

func TestBuildManifestsAcceptsAValidProductName(t *testing.T) {
	if _, err := cli.BuildManifests(valid()); err != nil {
		t.Fatalf("BuildManifests rejected a valid product name: %v", err)
	}
}

// Memory is rendered into nvidia.com/gpu.memory as a plain integer, so a
// negative quantity produces "-81920" — not a legal label value, and not a
// quantity of memory any hardware has.
func TestBuildManifestsRejectsNonPositiveMemory(t *testing.T) {
	for _, memory := range []string{"-80Gi", "0"} {
		opts := valid()
		opts.Memory = memory

		if _, err := cli.BuildManifests(opts); err == nil {
			t.Errorf("BuildManifests accepted memory %q", memory)
		}
	}
}

// --dry-run is documented as the offline validator, so emitting a manifest
// that kubectl apply will reject defeats its purpose.
func TestBuildManifestsRejectsNamesTheAPIServerWillNotAccept(t *testing.T) {
	for _, name := range []string{
		"My_Pool",   // underscore and uppercase
		"pool!",     // punctuation
		"-leading",  // leading dash
		"trailing-", // trailing dash
	} {
		opts := valid()
		opts.Name = name

		if _, err := cli.BuildManifests(opts); err == nil {
			t.Errorf("BuildManifests accepted name %q, which is not a DNS-1123 subdomain", name)
		}
	}
}

// The pool object is named "<name>-pool", so a name that is legal on its own
// can still overflow the 253-character limit once suffixed.
func TestBuildManifestsRejectsANameThatOverflowsOnceSuffixed(t *testing.T) {
	opts := valid()
	opts.Name = strings.Repeat("a", 253)

	if _, err := cli.BuildManifests(opts); err == nil {
		t.Error("BuildManifests accepted a name whose -pool suffix exceeds the length limit")
	}
}
