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

package v1alpha1

import (
	"testing"

	"k8s.io/utils/ptr"
)

// The advertise accessors exist because these fields are pointers, which they
// are because a defaulted bool with omitempty serializes an explicit false as
// absent and the API server defaults it straight back to true. Nil must read as
// the CRD's declared default, and an explicit false must survive.
func TestAdvertiseAccessors(t *testing.T) {
	cases := []struct {
		name    string
		spec    AdvertiseSpec
		wantDRA bool
		wantExt bool
	}{
		{
			name:    "unset reads as the declared default",
			spec:    AdvertiseSpec{},
			wantDRA: true,
			wantExt: true,
		},
		{
			name:    "explicit false survives",
			spec:    AdvertiseSpec{DRA: ptr.To(false), ExtendedResource: ptr.To(false)},
			wantDRA: false,
			wantExt: false,
		},
		{
			name:    "explicit true",
			spec:    AdvertiseSpec{DRA: ptr.To(true), ExtendedResource: ptr.To(true)},
			wantDRA: true,
			wantExt: true,
		},
		{
			name:    "paths are independent",
			spec:    AdvertiseSpec{DRA: ptr.To(false)},
			wantDRA: false,
			wantExt: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.DRAEnabled(); got != tc.wantDRA {
				t.Errorf("DRAEnabled() = %v, want %v", got, tc.wantDRA)
			}
			if got := tc.spec.ExtendedResourceEnabled(); got != tc.wantExt {
				t.Errorf("ExtendedResourceEnabled() = %v, want %v", got, tc.wantExt)
			}
		})
	}
}

func TestMIGEnabled(t *testing.T) {
	cases := map[SharingMode]bool{
		SharingModeMIG:  true,
		SharingModeNone: false,
		// The zero value reaches this method whenever a pool is built in
		// process rather than read back through the API server, so it must not
		// be mistaken for MIG.
		"": false,
	}

	for mode, want := range cases {
		spec := GPUPoolSpec{SharingMode: mode}
		if got := spec.MIGEnabled(); got != want {
			t.Errorf("SharingMode %q: MIGEnabled() = %v, want %v", mode, got, want)
		}
	}
}
