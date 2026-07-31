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

package mig

import "testing"

// TestParseProfileNameAgreesWithBuiltInTables is the test that justifies the
// function existing at all.
//
// Deriving a profile's shape from its name is only sound if the name really
// does describe the shape. Rather than assume that, this asserts it against
// every profile in every built-in table — the same numbers taken from NVIDIA's
// published documentation. If the two ever disagree, deriving is unsafe and
// this fails.
func TestParseProfileNameAgreesWithBuiltInTables(t *testing.T) {
	for table, want := range builtInTables {
		for _, p := range want.Profiles {
			got, ok := ParseProfileName(p.Name)
			if !ok {
				t.Errorf("%s: ParseProfileName(%q) = not ok, want a profile", table, p.Name)
				continue
			}
			if got.Slices != p.Slices {
				t.Errorf("%s: %s slices = %d, want %d", table, p.Name, got.Slices, p.Slices)
			}
			if got.Memory.Cmp(p.Memory) != 0 {
				t.Errorf("%s: %s memory = %s, want %s", table, p.Name, got.Memory.String(), p.Memory.String())
			}
			if got.Name != p.Name {
				t.Errorf("%s: name = %q, want %q", table, got.Name, p.Name)
			}
		}
	}
}

func TestParseProfileNameRejectsMalformed(t *testing.T) {
	// A profile ghostgpu has no table for still parses, which is the point:
	// capture has to describe hardware the built-in tables never listed.
	if got, ok := ParseProfileName("2g.24gb"); !ok || got.Slices != 2 || got.Memory.String() != "24Gi" {
		t.Errorf("ParseProfileName(2g.24gb) = %+v, %v; want 2 slices and 24Gi", got, ok)
	}

	for _, name := range []string{
		"",
		"1g",
		"10gb",
		"1g.10",
		"g.gb",
		"1g.10gb ",
		"1G.10GB",
		"0g.10gb",  // an instance consuming no compute slice does not exist
		"1g.0gb",   // nor one with no memory
		"1g.10gbx", // trailing junk must not be silently accepted
	} {
		if _, ok := ParseProfileName(name); ok {
			t.Errorf("ParseProfileName(%q) = ok, want rejected", name)
		}
	}
}
