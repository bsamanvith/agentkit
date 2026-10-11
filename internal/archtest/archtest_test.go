package archtest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
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

func goList(t *testing.T) []archtest.Package {
	t.Helper()
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = strings.TrimSpace(string(root))
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
