package helm_test

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var update = flag.Bool("update", false, "rewrite the golden render files")

// chartDir is the chart under test, relative to this package's directory.
const chartDir = ".."

func helmBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("helm")
	if err != nil {
		// The CI Helm job installs the binary; the ordinary Go test job skips
		// here rather than failing where Helm is unavailable.
		t.Skip("helm binary not found; install Helm to run chart render tests")
	}
	return path
}

// render runs helm template with a fixture and returns the rendered manifests.
func render(t *testing.T, valuesFile string, extra ...string) string {
	t.Helper()
	chart, err := filepath.Abs(chartDir)
	if err != nil {
		t.Fatalf("resolve chart dir: %v", err)
	}
	values, err := filepath.Abs(filepath.Join("..", "testdata", valuesFile))
	if err != nil {
		t.Fatalf("resolve values file: %v", err)
	}
	args := []string{
		"template", "sorovista", chart,
		"--namespace", "sorovista",
		"--values", values,
	}
	args = append(args, extra...)
	cmd := exec.Command(helmBinary(t), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func normalizeRender(output string) string {
	lines := strings.Split(output, "\n")
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "---" {
			for len(normalized) > 0 && normalized[len(normalized)-1] == "" {
				normalized = normalized[:len(normalized)-1]
			}
		}
		normalized = append(normalized, line)
	}
	return strings.Join(normalized, "\n")
}

func TestGoldenRender(t *testing.T) {
	tests := []struct {
		name       string
		valuesFile string
		goldenFile string
	}{
		{"standalone", "values-standalone.yaml", "golden-standalone.yaml"},
		{"upstream", "values-upstream.yaml", "golden-upstream.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRender(render(t, tt.valuesFile))
			goldenPath, err := filepath.Abs(filepath.Join("..", "testdata", tt.goldenFile))
			if err != nil {
				t.Fatalf("resolve golden file: %v", err)
			}

			if *update {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run go test ./... -run TestGoldenRender -update): %v", err)
			}
			want = []byte(normalizeRender(string(want)))
			if !bytes.Equal([]byte(got), want) {
				t.Fatalf("rendered chart differs from %s\n%s", goldenPath, lineDiff(string(want), got))
			}
		})
	}
}

// lineDiff reports the first differing lines, which is enough to review a
// template change without dumping the whole manifest.
func lineDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	var b strings.Builder
	shown := 0
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		fmt.Fprintf(&b, "line %d:\n  want: %s\n  got:  %s\n", i+1, w, g)
		shown++
		if shown == 10 {
			b.WriteString("... (truncated)\n")
			break
		}
	}
	return b.String()
}

// manifest is the slice of each rendered object these tests inspect.
type manifest struct {
	Kind       string            `yaml:"kind"`
	Metadata   manifestMetadata  `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
	StringData map[string]string `yaml:"stringData"`
	Spec       manifestSpec      `yaml:"spec"`
}

type manifestMetadata struct {
	Name string `yaml:"name"`
}

type manifestSpec struct {
	Template struct {
		Spec struct {
			Containers []container `yaml:"containers"`
		} `yaml:"spec"`
	} `yaml:"template"`
}

type container struct {
	Name string `yaml:"name"`
	Env  []struct {
		Name string `yaml:"name"`
	} `yaml:"env"`
	LivenessProbe  probe `yaml:"livenessProbe"`
	ReadinessProbe probe `yaml:"readinessProbe"`
}

type probe struct {
	HTTPGet struct {
		Path string `yaml:"path"`
	} `yaml:"httpGet"`
}

func parseManifests(t *testing.T, rendered string) []manifest {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	var docs []manifest
	for {
		var doc manifest
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse rendered manifest: %v", err)
		}
		if doc.Kind != "" {
			docs = append(docs, doc)
		}
	}
	return docs
}

// renderedEnv returns every environment variable name the manifests set: the
// ConfigMap keys plus the generated Secret's key (the Deployment injects both).
func renderedEnv(docs []manifest) map[string]string {
	env := map[string]string{}
	for _, doc := range docs {
		switch doc.Kind {
		case "ConfigMap":
			for k, v := range doc.Data {
				env[k] = v
			}
		case "Secret":
			for k, v := range doc.StringData {
				env[k] = v
			}
		}
	}
	return env
}

func deployment(t *testing.T, docs []manifest) manifest {
	t.Helper()
	for _, doc := range docs {
		if doc.Kind == "Deployment" {
			return doc
		}
	}
	t.Fatal("rendered chart has no Deployment")
	return manifest{}
}

// configEnvVars extracts every environment variable internal/config reads,
// including the getenv helper used for defaults.
var configEnvVar = regexp.MustCompile(`(?:os\.Getenv|getenv)\(\s*"([A-Z][A-Z0-9_]*)"`)

func configEnvVars(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "internal", "config")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/config: %v", err)
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range configEnvVar.FindAllStringSubmatch(string(src), -1) {
			seen[match[1]] = true
		}
	}

	vars := make([]string, 0, len(seen))
	for name := range seen {
		vars = append(vars, name)
	}
	sort.Strings(vars)
	if len(vars) == 0 {
		t.Fatal("found no environment variables in internal/config")
	}
	return vars
}

// TestChartSetsEveryConfigEnvVar is the drift guard: adding an environment
// variable to internal/config without a values key and ConfigMap/Secret
// mapping fails here, instead of shipping a chart that silently cannot
// configure the new setting.
func TestChartSetsEveryConfigEnvVar(t *testing.T) {
	standalone := parseManifests(t, render(t, "values-standalone.yaml"))
	upstream := parseManifests(t, render(t, "values-upstream.yaml"))

	standaloneEnv := renderedEnv(standalone)
	upstreamEnv := renderedEnv(upstream)

	union := map[string]bool{}
	for name := range standaloneEnv {
		union[name] = true
	}
	for name := range upstreamEnv {
		union[name] = true
	}

	want := configEnvVars(t)
	for _, name := range want {
		if !union[name] {
			t.Errorf("internal/config reads %s but the chart never sets it; add a values key and map it in the ConfigMap or Secret", name)
		}
	}

	// The guard runs both ways: an env var the chart sets that config does
	// not read is dead configuration and should be removed.
	for name := range union {
		found := false
		for _, wanted := range want {
			if name == wanted {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("chart sets %s but internal/config never reads it", name)
		}
	}
}

func TestModeSpecificSettingsAreOmitted(t *testing.T) {
	standalone := renderedEnv(parseManifests(t, render(t, "values-standalone.yaml")))
	upstream := renderedEnv(parseManifests(t, render(t, "values-upstream.yaml")))

	for _, name := range []string{"SOROTRAIL_URL"} {
		if _, ok := standalone[name]; ok {
			t.Errorf("standalone render must omit %s, got %q", name, standalone[name])
		}
	}
	for _, name := range []string{"DATABASE_URL", "RPC_URL", "NETWORK", "NETWORK_PASSPHRASE", "POLL_INTERVAL", "WATCHED_CONTRACTS", "START_LEDGER", "RETENTION_LEDGERS"} {
		if _, ok := upstream[name]; ok {
			t.Errorf("upstream render must omit %s, got %q", name, upstream[name])
		}
	}

	for name, value := range standalone {
		if value == "" {
			t.Errorf("standalone render sets %s to an empty string; omit it instead", name)
		}
	}
	for name, value := range upstream {
		if value == "" {
			t.Errorf("upstream render sets %s to an empty string; omit it instead", name)
		}
	}
}

func TestDatabaseURLIsSecretAndProbesUseDocumentedEndpoints(t *testing.T) {
	docs := parseManifests(t, render(t, "values-standalone.yaml"))

	configMap := manifest{}
	secret := manifest{}
	for _, doc := range docs {
		switch doc.Kind {
		case "ConfigMap":
			configMap = doc
		case "Secret":
			secret = doc
		}
	}

	if _, ok := configMap.Data["DATABASE_URL"]; ok {
		t.Fatal("DATABASE_URL is rendered into a ConfigMap; it can carry a password and belongs in a Secret")
	}
	if got := secret.StringData["DATABASE_URL"]; got == "" {
		t.Fatal("DATABASE_URL is missing from the generated Secret")
	}

	dep := deployment(t, docs)
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("Deployment has no containers")
	}
	c := dep.Spec.Template.Spec.Containers[0]

	if got := c.LivenessProbe.HTTPGet.Path; got != "/livez" {
		t.Errorf("livenessProbe path = %q, want /livez (a dead backend must not restart the pod)", got)
	}
	if got := c.ReadinessProbe.HTTPGet.Path; got != "/readyz" {
		t.Errorf("readinessProbe path = %q, want /readyz (readiness consults the event source)", got)
	}

	hasDatabaseEnv := false
	for _, env := range c.Env {
		if env.Name == "DATABASE_URL" {
			hasDatabaseEnv = true
		}
	}
	if !hasDatabaseEnv {
		t.Fatal("standalone Deployment does not inject DATABASE_URL")
	}
}

func TestExistingSecretReferenceIsUsedInsteadOfGeneratedSecret(t *testing.T) {
	rendered := render(t, "values-standalone.yaml",
		"--set", "database.url=",
		"--set", "database.existingSecret=my-db",
		"--set", "database.existingSecretKey=dsn",
	)

	var sawSecret, sawDatabaseEnv bool
	for _, doc := range parseManifests(t, rendered) {
		if doc.Kind == "Secret" {
			sawSecret = true
		}
		if doc.Kind == "Deployment" {
			container := doc.Spec.Template.Spec.Containers[0]
			for _, env := range container.Env {
				if env.Name == "DATABASE_URL" {
					sawDatabaseEnv = true
				}
			}
		}
	}
	if sawSecret {
		t.Error("chart generated a Secret even though database.existingSecret is set")
	}
	if !sawDatabaseEnv {
		t.Error("Deployment does not reference the existing database Secret")
	}
}
