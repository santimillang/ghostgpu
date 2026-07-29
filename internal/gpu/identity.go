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

// Package gpu constructs the simulated-GPU objects ghostgpu publishes:
// device identities, DRA ResourceSlices, and GPU Feature Discovery labels.
//
// Everything here is a pure function of its inputs. Identities in particular
// are deterministic: a restarted operator must republish byte-identical
// objects rather than churning ResourceSlices, and tests must never flake on
// a regenerated UUID.
package gpu

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// DeviceName returns the DRA device name for a simulated GPU.
//
// DRA device names must be valid DNS labels and are only required to be
// unique within a pool. ghostgpu uses one pool per node, so the index alone
// is sufficient and the node name is intentionally unused — including it
// would make names longer without making them more unique.
func DeviceName(_ string, index int32) string {
	return fmt.Sprintf("gpu-%d", index)
}

// DeviceUUID derives a stable, NVIDIA-shaped GPU UUID from a node name and
// device index.
//
// The value is a hash, not a random UUID, so it survives operator restarts
// unchanged. It is not a real RFC 4122 UUID and does not claim to be: it
// only needs to be stable, unique per (node, index), and shaped like the
// GPU-<8>-<4>-<4>-<4>-<12> identifiers that real tooling expects to parse.
func DeviceUUID(nodeName string, index int32) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "ghostgpu/%s/%d", nodeName, index))
	h := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("GPU-%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// NVLinkDomain returns the NVLink domain a GPU index belongs to, or "" when
// domains are disabled (domainSize <= 0).
//
// This models topology structure only. Bandwidth and latency are deliberately
// not simulated: no scheduler consumes them today, so modeling them would add
// complexity without adding testing value.
func NVLinkDomain(index, domainSize int32) string {
	if domainSize <= 0 {
		return ""
	}
	return fmt.Sprintf("domain-%d", index/domainSize)
}

// NUMANode returns the NUMA node a GPU index is local to.
//
// Locality is derived from the NVLink domain because on real hardware GPUs
// sharing an NVLink domain sit behind the same PCIe root complex. When
// domains are disabled every device reports node 0, which is what a
// single-socket machine would report.
func NUMANode(index, domainSize int32) int64 {
	if domainSize <= 0 {
		return 0
	}
	return int64(index / domainSize)
}
