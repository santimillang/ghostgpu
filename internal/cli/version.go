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

package cli

import "fmt"

// BuildInfo describes the binary the user is actually running.
//
// The fields are populated by ldflags at release time and left empty by a plain
// `go build`, so every field has to render sensibly when unset.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// VersionLine renders BuildInfo as the one line `ghostgpu version` prints.
//
// Unset fields are named rather than blank. An empty version reads like a
// released binary that forgot to say which release, which is the one thing this
// command exists to prevent.
func VersionLine(b BuildInfo) string {
	version := b.Version
	if version == "" {
		version = "dev"
	}

	commit := "unknown commit"
	if b.Commit != "" {
		commit = b.Commit
	}

	built := "built at an unknown time"
	if b.Date != "" {
		built = "built " + b.Date
	}

	return fmt.Sprintf("ghostgpu %s (%s, %s)", version, commit, built)
}
