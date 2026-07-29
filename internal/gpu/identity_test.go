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

package gpu

import (
	"fmt"
	"regexp"
	"testing"
)

const (
	nodeA    = "node-a"
	nodeB    = "node-b"
	domain0  = "domain-0"
	domain1  = "domain-1"
	maxIndex = 128 // the DRA per-ResourceSlice device limit
)

func TestDeviceName(t *testing.T) {
	cases := []struct {
		node  string
		index int32
		want  string
	}{
		{nodeA, 0, "gpu-0"},
		{nodeA, 7, "gpu-7"},
		{nodeB, 7, "gpu-7"}, // names are pool-local, so the node does not appear
	}
	for _, c := range cases {
		if got := DeviceName(c.node, c.index); got != c.want {
			t.Errorf("DeviceName(%q, %d) = %q, want %q", c.node, c.index, got, c.want)
		}
	}
}

// DRA device names must be valid DNS labels (RFC 1123).
func TestDeviceNameIsValidDNSLabel(t *testing.T) {
	dnsLabel := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	for i := range int32(maxIndex) {
		name := DeviceName(nodeA, i)
		if !dnsLabel.MatchString(name) {
			t.Errorf("DeviceName(_, %d) = %q, not a valid DNS label", i, name)
		}
		if len(name) > 63 {
			t.Errorf("DeviceName(_, %d) = %q, exceeds 63 chars", i, name)
		}
	}
}

// Determinism is a hard requirement: a restarted operator must republish
// identical identities rather than churning ResourceSlices, and tests must
// never flake on a regenerated UUID.
func TestDeviceUUIDIsDeterministic(t *testing.T) {
	first := DeviceUUID(nodeA, 3)
	second := DeviceUUID(nodeA, 3)
	if first != second {
		t.Errorf("DeviceUUID not deterministic: %q != %q", first, second)
	}
}

func TestDeviceUUIDShape(t *testing.T) {
	// Real NVIDIA UUIDs look like GPU-<8>-<4>-<4>-<4>-<12> hex.
	shape := regexp.MustCompile(`^GPU-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	got := DeviceUUID(nodeA, 0)
	if !shape.MatchString(got) {
		t.Errorf("DeviceUUID = %q, does not match NVIDIA UUID shape", got)
	}
}

func TestDeviceUUIDIsUniquePerNodeAndIndex(t *testing.T) {
	seen := map[string]string{}
	for _, node := range []string{nodeA, nodeB, "node-c"} {
		for i := range int32(8) {
			id := DeviceUUID(node, i)
			key := fmt.Sprintf("%s/%d", node, i)
			if prev, dup := seen[id]; dup {
				t.Fatalf("UUID collision: %s and %s both produced %q", prev, key, id)
			}
			seen[id] = key
		}
	}
	if len(seen) != 24 {
		t.Errorf("got %d unique UUIDs, want 24", len(seen))
	}
}

// Two nodes at the same index must differ, or per-GPU metrics would collide
// across a fleet.
func TestDeviceUUIDDiffersAcrossNodes(t *testing.T) {
	if DeviceUUID(nodeA, 0) == DeviceUUID(nodeB, 0) {
		t.Error("DeviceUUID identical across different nodes at the same index")
	}
}

func TestNVLinkDomain(t *testing.T) {
	cases := []struct {
		name        string
		index, size int32
		want        string
	}{
		{"first domain, first gpu", 0, 4, domain0},
		{"first domain, last gpu", 3, 4, domain0},
		{"second domain, first gpu", 4, 4, domain1},
		{"second domain, last gpu", 7, 4, domain1},
		{"domain of one", 5, 1, "domain-5"},
		{"all gpus in one domain", 7, 8, domain0},
		{"disabled", 5, 0, ""},
		{"negative size treated as disabled", 5, -1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NVLinkDomain(c.index, c.size); got != c.want {
				t.Errorf("NVLinkDomain(%d, %d) = %q, want %q", c.index, c.size, got, c.want)
			}
		})
	}
}

// NUMA locality is derived from the NVLink domain: GPUs sharing an NVLink
// domain sit behind the same PCIe root complex on real hardware.
func TestNUMANode(t *testing.T) {
	cases := []struct {
		name        string
		index, size int32
		want        int64
	}{
		{"first domain", 0, 4, 0},
		{"first domain, last gpu", 3, 4, 0},
		{"second domain", 4, 4, 1},
		{"disabled falls back to node 0", 5, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NUMANode(c.index, c.size); got != c.want {
				t.Errorf("NUMANode(%d, %d) = %d, want %d", c.index, c.size, got, c.want)
			}
		})
	}
}
