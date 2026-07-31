//go:build chart

// Package chart_test asserts that the Helm chart and install.yaml describe the
// same operator. They come from different generators, so nothing but a test
// stops them describing different ones.
package chart_test

import (
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// kindsOf runs a command and returns the "Kind/name" of every document it
// emits, excluding Namespace: Helm creates namespaces through
// --create-namespace rather than as a chart object, so a difference there is
// expected rather than drift.
func kindsOf(t *testing.T, name string, args ...string) map[string]bool {
	t.Helper()

	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("running %s %v: %v", name, args, err)
	}

	kinds := map[string]bool{}
	for _, doc := range strings.Split(string(out), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("parsing a rendered document: %v", err)
		}
		if o.Kind == "" || o.Kind == "Namespace" {
			continue
		}
		kinds[o.Kind+"/"+o.Metadata.Name] = true
	}
	return kinds
}

func TestChartAndInstallerDescribeTheSameObjects(t *testing.T) {
	// go test runs the test binary with its working directory set to this
	// package's source directory (test/chart/), not the repo root the
	// generators were run from, so the repo-root-relative paths below have to
	// climb back out of it.
	installer := kindsOf(t, "cat", "../../dist/install.yaml")
	// --include-crds is required: Helm's crds/ directory is special-cased and
	// "helm template" omits it by default, which would make the CRDs -
	// exactly the objects most important to compare - look like drift on
	// every run.
	chart := kindsOf(t, "../../bin/helm", "template", "ghostgpu", "../../charts/ghostgpu",
		"--namespace", "ghostgpu-system", "--include-crds")

	for k := range installer {
		if !chart[k] {
			t.Errorf("install.yaml has %s but the chart does not", k)
		}
	}
	for k := range chart {
		if !installer[k] {
			t.Errorf("the chart has %s but install.yaml does not", k)
		}
	}
}
