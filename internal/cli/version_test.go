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
	"testing"

	"github.com/santimillang/ghostgpu/internal/cli"
)

// A released binary reports exactly what it was built from, because that is the
// first line of any bug report.
func TestVersionLineReportsInjectedBuildInfo(t *testing.T) {
	got := cli.VersionLine(cli.BuildInfo{
		Version: "v0.1.0",
		Commit:  "0b289ac",
		Date:    "2026-07-31T12:00:00Z",
	})

	want := "ghostgpu v0.1.0 (0b289ac, built 2026-07-31T12:00:00Z)"
	if got != want {
		t.Errorf("VersionLine() = %q, want %q", got, want)
	}
}

// A binary built with `make build-cli` or `go build` has nothing injected. It
// must say so rather than print an empty version that reads like a released
// one, because "ghostgpu  ()" in an issue is worse than no version at all.
func TestVersionLineSaysDevWhenNothingWasInjected(t *testing.T) {
	got := cli.VersionLine(cli.BuildInfo{})

	want := "ghostgpu dev (unknown commit, built at an unknown time)"
	if got != want {
		t.Errorf("VersionLine() = %q, want %q", got, want)
	}
}

// Partial injection is the realistic failure: a build system that sets the
// version but not the date should still produce a readable line.
func TestVersionLineFillsOnlyTheMissingParts(t *testing.T) {
	got := cli.VersionLine(cli.BuildInfo{Version: "v0.2.0"})

	want := "ghostgpu v0.2.0 (unknown commit, built at an unknown time)"
	if got != want {
		t.Errorf("VersionLine() = %q, want %q", got, want)
	}
}
