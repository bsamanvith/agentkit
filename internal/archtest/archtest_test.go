package archtest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/internal/archtest"
)

const mod = "example.com/m"

func rules() archtest.Rules {
	return archtest.Rules{
		Module:     mod,
		ThirdParty: map[string][]string{"pkg/a": {"example.org/lib"}},
		TestOnly:   []string{"example.org/cmp"},
		Banned:     []string{"go.uber.org/mock"},
		ModuleImports: map[string][]string{
			"pkg/store":        {"pkg/llm"},
			"pkg/store/sqlite": {"pkg/llm", "pkg/clock"},
		},
		SharedChildren: map[string][]string{"pkg/api": {"pkg/api/wire"}},
	}
}

// TestRepository is the real check: the module's import graph obeys its rules.
func TestRepository(t *testing.T) {
	t.Parallel()
	pkgs := goList(t)
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	for _, v := range archtest.Check(archtest.ModuleRules(), pkgs) {
		t.Error(v)
	}
	vs, err := archtest.CheckConnectors(moduleRoot(t), archtest.ModuleRules())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		t.Error(v)
	}
}

// TestConnectorRules: a connector's tests must run the kit's suite and fuzz
// target, through the kit and not a lookalike, and it must have cassettes.
// A pending connector is exempt while it fails, and its entry is stale once
// it passes.
func TestConnectorRules(t *testing.T) {
	t.Parallel()
	r := rules()
	r.PendingConnectors = map[string]string{"connectors/lazy": "adopting", "connectors/good": "adopted already"}
	got, err := archtest.CheckConnectors(filepath.Join("testdata", "repo"), r)
	if err != nil {
		t.Fatal(err)
	}
	// In directory order: bare, good, imposter (lazy is pending).
	var want []archtest.Violation
	for _, pkg := range []string{"connectors/bare", "connectors/good", "connectors/imposter"} {
		if pkg == "connectors/good" {
			want = append(want, archtest.Violation{Rule: archtest.RuleConnectorPending, Package: pkg, Detail: "passes the connector rules: remove it from PendingConnectors"})
			continue
		}
		want = append(want,
			archtest.Violation{Rule: archtest.RuleConnectorSuite, Package: pkg, Detail: "has no test that calls toolsettest.RunConnector"},
			archtest.Violation{Rule: archtest.RuleConnectorFuzz, Package: pkg, Detail: "has no fuzz target that calls toolsettest.FuzzResponses"},
		)
		if pkg == "connectors/bare" {
			want = append(want, archtest.Violation{Rule: archtest.RuleConnectorCassettes, Package: pkg, Detail: "has no cassette in testdata/cassettes (make record-cassettes)"})
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("violations (-want +got):\n%s", diff)
	}
}

func TestRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pkg  archtest.Package
		want []archtest.Violation
	}{
		{
			name: "pkg imports cmd",
			pkg:  archtest.Package{ImportPath: mod + "/pkg/a", Imports: []string{mod + "/cmd/agentkit"}},
			want: []archtest.Violation{{Rule: archtest.RuleNoCmdImports, Package: "pkg/a", Import: mod + "/cmd/agentkit"}},
		},
		{
			name: "parent imports its own subpackage",
			pkg:  archtest.Package{ImportPath: mod + "/pkg/store", Imports: []string{mod + "/pkg/store/sqlite"}},
			want: []archtest.Violation{{Rule: archtest.RuleNoChildImports, Package: "pkg/store", Import: mod + "/pkg/store/sqlite"}},
		},
		{
			name: "only a listed shared child may be imported by its parent",
			pkg:  archtest.Package{ImportPath: mod + "/pkg/api", Imports: []string{mod + "/pkg/api/wire", mod + "/pkg/api/impl"}},
			want: []archtest.Violation{{Rule: archtest.RuleNoChildImports, Package: "pkg/api", Import: mod + "/pkg/api/impl"}},
		},
		{
			name: "non-test code imports a test helper",
			pkg: archtest.Package{ImportPath: mod + "/pkg/a", Imports: []string{
				mod + "/pkg/b/btest", mod + "/internal/testkit", "testing", "net/http/httptest",
			}},
			want: []archtest.Violation{
				{Rule: archtest.RuleNoTestHelpers, Package: "pkg/a", Import: mod + "/internal/testkit"},
				{Rule: archtest.RuleNoTestHelpers, Package: "pkg/a", Import: mod + "/pkg/b/btest"},
				{Rule: archtest.RuleNoTestHelpers, Package: "pkg/a", Import: "net/http/httptest"},
				{Rule: archtest.RuleNoTestHelpers, Package: "pkg/a", Import: "testing"},
			},
		},
		{
			name: "third-party import not on the allowlist",
			pkg: archtest.Package{
				ImportPath: mod + "/pkg/a",
				Imports:    []string{"example.org/lib", "example.org/other", "example.org/cmp"},
			},
			want: []archtest.Violation{
				{Rule: archtest.RuleThirdPartyAllowed, Package: "pkg/a", Import: "example.org/cmp"},
				{Rule: archtest.RuleThirdPartyAllowed, Package: "pkg/a", Import: "example.org/other"},
			},
		},
		{
			name: "mocking framework in a test",
			pkg:  archtest.Package{ImportPath: mod + "/pkg/a", XTestImports: []string{"go.uber.org/mock/gomock"}},
			want: []archtest.Violation{
				{Rule: archtest.RuleBanned, Package: "pkg/a", Import: "go.uber.org/mock/gomock"},
				{Rule: archtest.RuleThirdPartyAllowed, Package: "pkg/a", Import: "go.uber.org/mock/gomock"},
			},
		},
		{
			name: "allowed imports produce no violations",
			pkg: archtest.Package{
				ImportPath:   mod + "/pkg/a",
				Imports:      []string{"context", "example.org/lib", mod + "/pkg/b", mod + "/internal/x"},
				TestImports:  []string{"testing", "example.org/cmp"},
				XTestImports: []string{mod + "/pkg/a/atest", mod + "/internal/testkit"},
			},
		},
		{
			name: "store imports the harness",
			pkg: archtest.Package{
				ImportPath:   mod + "/pkg/store/memory",
				Imports:      []string{mod + "/pkg/store", mod + "/pkg/llm", mod + "/pkg/harness"},
				XTestImports: []string{mod + "/pkg/store/storetest", mod + "/pkg/harness/sub"},
			},
			want: []archtest.Violation{
				{Rule: archtest.RuleModuleAllowed, Package: "pkg/store/memory", Import: mod + "/pkg/harness"},
				{Rule: archtest.RuleModuleAllowed, Package: "pkg/store/memory", Import: mod + "/pkg/harness/sub"},
			},
		},
		{
			name: "the longest tree's allowlist applies",
			pkg: archtest.Package{
				ImportPath: mod + "/pkg/store/sqlite/migrate",
				Imports:    []string{mod + "/pkg/clock", mod + "/pkg/store", mod + "/pkg/tool", "context"},
			},
			want: []archtest.Violation{
				{Rule: archtest.RuleModuleAllowed, Package: "pkg/store/sqlite/migrate", Import: mod + "/pkg/store"},
				{Rule: archtest.RuleModuleAllowed, Package: "pkg/store/sqlite/migrate", Import: mod + "/pkg/tool"},
			},
		},
		{
			name: "a package whose name only ends in test is not a test helper",
			pkg:  archtest.Package{ImportPath: mod + "/pkg/latest", Imports: []string{"testing", mod + "/pkg/b/latest"}},
			want: []archtest.Violation{
				{Rule: archtest.RuleNoTestHelpers, Package: "pkg/latest", Import: "testing"},
			},
		},
		{
			name: "test helper packages may import testing and test-only libraries",
			pkg: archtest.Package{
				ImportPath: mod + "/pkg/a/atest",
				Imports:    []string{"testing", mod + "/internal/testkit", "example.org/cmp"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := archtest.Check(rules(), []archtest.Package{tt.pkg})
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("violations (-want +got):\n%s", diff)
			}
		})
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	return strings.TrimSpace(string(root))
}

func goList(t *testing.T) []archtest.Package {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = moduleRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []archtest.Package
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p archtest.Package
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}
