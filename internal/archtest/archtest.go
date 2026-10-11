// Package archtest enforces the dependency rules from docs/ENGINEERING.md §1.4
// on the module's import graph. The test in this package runs `go list` over
// the whole module and fails on any violation.
//
// To give a package a new third-party dependency, add it to [Allowlist].
package archtest

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Package is the subset of `go list -json` output the rules need.
type Package struct {
	ImportPath   string
	Imports      []string // non-test imports
	TestImports  []string // imports of package foo tests
	XTestImports []string // imports of package foo_test tests
}

// Rules configures [Check].
type Rules struct {
	// Module is the module path, e.g. "github.com/bsamanvith/agentkit".
	Module string
	// ThirdParty lists, per package (path relative to Module), the third-party
	// packages it may import from any of its files.
	ThirdParty map[string][]string
	// TestOnly lists third-party packages any package may import from tests.
	TestOnly []string
	// Banned lists import path prefixes no file may import, tests included.
	Banned []string
}

// Violation is one broken rule.
type Violation struct {
	Rule    string // short rule id, e.g. "no-cmd-imports"
	Package string // relative package path
	Import  string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s imports %s", v.Rule, v.Package, v.Import)
}

// Rule ids reported in [Violation.Rule].
const (
	RuleNoCmdImports      = "no-cmd-imports"
	RuleNoChildImports    = "no-child-imports"
	RuleNoTestHelpers     = "no-test-helpers-in-non-test-code"
	RuleThirdPartyAllowed = "third-party-allowlist"
	RuleBanned            = "banned-import"
)

// Check returns every violation of the rules in pkgs, sorted for stable output.
func Check(rules Rules, pkgs []Package) []Violation {
	var out []Violation
	add := func(rule, pkg, imp string) {
		out = append(out, Violation{Rule: rule, Package: pkg, Import: imp})
	}

	for _, p := range pkgs {
		rel := relative(rules.Module, p.ImportPath)
		helper := isTestHelper(rel)

		for _, imp := range p.Imports {
			impRel, internal := relativeOK(rules.Module, imp)
			if internal && (impRel == "cmd" || strings.HasPrefix(impRel, "cmd/")) {
				add(RuleNoCmdImports, rel, imp)
			}
			if internal && strings.HasPrefix(impRel, rel+"/") {
				add(RuleNoChildImports, rel, imp)
			}
			if !helper && isTestHelperImport(rules.Module, imp) {
				add(RuleNoTestHelpers, rel, imp)
			}
		}

		allowed := rules.ThirdParty[rel]
		check3p := func(imp string, inTest bool) {
			if isStdlib(imp) || isModule(rules.Module, imp) {
				return
			}
			if slices.Contains(allowed, imp) || (inTest && slices.Contains(rules.TestOnly, imp)) {
				return
			}
			add(RuleThirdPartyAllowed, rel, imp)
		}
		for _, imp := range p.Imports {
			check3p(imp, helper) // test helpers count as test code
		}
		for _, imp := range concat(p.TestImports, p.XTestImports) {
			check3p(imp, true)
		}

		for _, imp := range concat(p.Imports, p.TestImports, p.XTestImports) {
			for _, b := range rules.Banned {
				if imp == b || strings.HasPrefix(imp, b+"/") {
					add(RuleBanned, rel, imp)
				}
			}
		}
	}

	out = dedupe(out)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// isTestHelper reports whether a module package is itself test support code
// (an xxxtest package or internal/testkit); such packages may import testing.
func isTestHelper(rel string) bool {
	last := rel[strings.LastIndex(rel, "/")+1:]
	return strings.HasSuffix(last, "test") || rel == "internal/testkit" || strings.HasPrefix(rel, "internal/testkit/")
}

func isTestHelperImport(module, imp string) bool {
	if imp == "testing" || strings.HasPrefix(imp, "testing/") {
		return true
	}
	if rel, ok := relativeOK(module, imp); ok {
		return isTestHelper(rel)
	}
	last := imp[strings.LastIndex(imp, "/")+1:]
	return isStdlib(imp) && strings.HasSuffix(last, "test") // e.g. net/http/httptest
}

// isStdlib uses the go command's own heuristic: standard library import paths
// have no dot in their first element.
func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func isModule(module, imp string) bool {
	_, ok := relativeOK(module, imp)
	return ok
}

func relativeOK(module, imp string) (string, bool) {
	if imp == module {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(imp, module+"/"); ok {
		return rest, true
	}
	return "", false
}

func relative(module, imp string) string {
	if rel, ok := relativeOK(module, imp); ok {
		return rel
	}
	return imp
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func dedupe(vs []Violation) []Violation {
	seen := make(map[Violation]bool, len(vs))
	out := vs[:0]
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
