//go:build chart

// Package chart_test asserts that the Helm chart and install.yaml describe the
// same operator. They come from different generators, so nothing but a test
// stops them describing different ones. Same set of objects is necessary but
// not sufficient: two ClusterRoles with the same name can still grant
// different verbs, and two Deployments can still run different images. Both
// are exactly the kind of divergence this test exists to catch, so they are
// asserted explicitly below rather than assumed from the object list matching.
package chart_test

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// policyRule mirrors the fields of rbacv1.PolicyRule that determine what
// access a Role or ClusterRole actually grants.
type policyRule struct {
	APIGroups       []string `json:"apiGroups,omitempty"`
	Resources       []string `json:"resources,omitempty"`
	ResourceNames   []string `json:"resourceNames,omitempty"`
	Verbs           []string `json:"verbs,omitempty"`
	NonResourceURLs []string `json:"nonResourceURLs,omitempty"`
}

type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	// Rules is populated on Role and ClusterRole documents.
	Rules []policyRule `json:"rules,omitempty"`
	// Spec.Template.Spec.Containers is populated on Deployment documents.
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec,omitempty"`
}

// renderObjects runs a command and parses every YAML document it emits into
// an object. Empty documents (a trailing "---" produces one) are skipped;
// everything else is returned regardless of kind, so callers can pull
// whichever fields they need out of the same render.
func renderObjects(t *testing.T, name string, args ...string) []object {
	t.Helper()

	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("running %s %v: %v", name, args, err)
	}

	var objs []object
	for _, doc := range strings.Split(string(out), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var o object
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("parsing a rendered document: %v", err)
		}
		if o.Kind == "" {
			continue
		}
		objs = append(objs, o)
	}
	return objs
}

// installerObjects runs kustomize's output through cat so it goes through the
// same rendering path as the chart: dist/install.yaml must already exist
// (built by `make build-installer`).
//
// go test runs the test binary with its working directory set to this
// package's source directory (test/chart/), not the repo root the
// generators were run from, so the repo-root-relative paths here have to
// climb back out of it.
func installerObjects(t *testing.T) []object {
	t.Helper()
	return renderObjects(t, "cat", "../../dist/install.yaml")
}

// chartObjects renders the chart, optionally overriding values (e.g. the
// image) with extra --set arguments.
//
// --include-crds is required: Helm's crds/ directory is special-cased and
// "helm template" omits it by default, which would make the CRDs - exactly
// the objects most important to compare - look like drift on every run.
func chartObjects(t *testing.T, extraArgs ...string) []object {
	t.Helper()
	args := []string{"template", "ghostgpu", "../../charts/ghostgpu",
		"--namespace", "ghostgpu-system", "--include-crds"}
	args = append(args, extraArgs...)
	return renderObjects(t, "../../bin/helm", args...)
}

// kindsOf returns the "Kind/name" of every object, excluding Namespace:
// Helm creates namespaces through --create-namespace rather than as a chart
// object, so a difference there is expected rather than drift.
func kindsOf(objs []object) map[string]bool {
	kinds := map[string]bool{}
	for _, o := range objs {
		if o.Kind == "Namespace" {
			continue
		}
		kinds[o.Kind+"/"+o.Metadata.Name] = true
	}
	return kinds
}

func TestChartAndInstallerDescribeTheSameObjects(t *testing.T) {
	installer := kindsOf(installerObjects(t))
	chart := kindsOf(chartObjects(t))

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

// rulesOf returns the PolicyRules of every Role and ClusterRole, keyed by
// "Kind/name" so a Role and a ClusterRole that happen to share a name are
// not conflated.
func rulesOf(objs []object) map[string][]policyRule {
	rules := map[string][]policyRule{}
	for _, o := range objs {
		if o.Kind == "Role" || o.Kind == "ClusterRole" {
			rules[o.Kind+"/"+o.Metadata.Name] = o.Rules
		}
	}
	return rules
}

// canonicalRules renders a set of PolicyRules as a deterministic string,
// insensitive to the order of the rules and the order of the strings within
// each rule, so two RBAC grants that are equivalent but were emitted in a
// different order still compare equal. When two roles do differ, the
// returned strings make a readable diff in the test failure.
func canonicalRules(rules []policyRule) string {
	lines := make([]string, 0, len(rules))
	for _, r := range rules {
		sort.Strings(r.APIGroups)
		sort.Strings(r.Resources)
		sort.Strings(r.ResourceNames)
		sort.Strings(r.Verbs)
		sort.Strings(r.NonResourceURLs)
		lines = append(lines, fmt.Sprintf(
			"apiGroups=%v resources=%v resourceNames=%v verbs=%v nonResourceURLs=%v",
			r.APIGroups, r.Resources, r.ResourceNames, r.Verbs, r.NonResourceURLs))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestChartAndInstallerAgreeOnRBAC(t *testing.T) {
	installerRules := rulesOf(installerObjects(t))
	chartRules := rulesOf(chartObjects(t))

	// A role missing from one side entirely is already reported by
	// TestChartAndInstallerDescribeTheSameObjects; here we only compare roles
	// present in both.
	for name, want := range installerRules {
		got, ok := chartRules[name]
		if !ok {
			continue
		}
		wantCanon, gotCanon := canonicalRules(want), canonicalRules(got)
		if wantCanon != gotCanon {
			t.Errorf("RBAC rules for %s differ:\ninstall.yaml:\n%s\n\nchart:\n%s",
				name, wantCanon, gotCanon)
		}
	}
}

// imagesOf returns the container images of every Deployment, keyed by
// "deployment-name/container-name" so multi-container Deployments compare
// each container independently.
func imagesOf(objs []object) map[string]string {
	images := map[string]string{}
	for _, o := range objs {
		if o.Kind != "Deployment" {
			continue
		}
		for _, c := range o.Spec.Template.Spec.Containers {
			images[o.Metadata.Name+"/"+c.Name] = c.Image
		}
	}
	return images
}

func TestChartAndInstallerAgreeOnImage(t *testing.T) {
	installerImages := imagesOf(installerObjects(t))
	if len(installerImages) == 0 {
		t.Fatal("install.yaml has no Deployment containers to compare")
	}

	// The chart's default values render image.tag as .Chart.AppVersion, while
	// install.yaml was built with whatever IMG= the caller passed to
	// `make build-installer` - comparing the chart's defaults against that
	// would produce a false failure on every version bump. Instead, read
	// whichever image install.yaml actually has and ask the chart to render
	// that same image via --set, so the assertion is "the chart is *capable*
	// of producing the exact image install.yaml has", which is the thing
	// that actually matters: does the same image reference reach the
	// container.
	for key, wantImage := range installerImages {
		repo, tag, ok := strings.Cut(wantImage, ":")
		if !ok {
			t.Fatalf("install.yaml image %q for %s has no :tag", wantImage, key)
		}

		chartImages := imagesOf(chartObjects(t,
			"--set", "image.repository="+repo,
			"--set", "image.tag="+tag))

		gotImage, ok := chartImages[key]
		if !ok {
			// Missing entirely is already reported by
			// TestChartAndInstallerDescribeTheSameObjects.
			continue
		}
		if gotImage != wantImage {
			t.Errorf("image for %s: install.yaml has %q, chart rendered with matching --set values produced %q",
				key, wantImage, gotImage)
		}
	}
}
