// Package archtest enforces the dependency rules from docs/ENGINEERING.md §1.4
// on the module's import graph. The test in this package runs `go list` over
// the whole module and fails on any violation. [CheckConnectors] checks that
// every connector is tested with the connector test kit.
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
	// ModuleImports lists, per package tree (a path relative to Module and
	// every package under it), the module packages its files may import,
	// tests included. Packages of the tree itself are always allowed. A tree
	// with no entry may import any module package.
	ModuleImports map[string][]string
	// PendingConnectors exempts connectors (paths relative to Module) from
	// [CheckConnectors], each with the reason, until they adopt the test kit.
	// An entry for a connector that passes fails the check.
	PendingConnectors map[string]string
}

// Violation is one broken rule.
type Violation struct {
	Rule    string // short rule id, e.g. "no-cmd-imports"
	Package string // relative package path
	Import  string
	Detail  string // what is wrong, for a rule that is not about an import
}

func (v Violation) String() string {
	if v.Detail != "" {
		return fmt.Sprintf("%s: %s %s", v.Rule, v.Package, v.Detail)
	}
	return fmt.Sprintf("%s: %s imports %s", v.Rule, v.Package, v.Import)
}

// Rule ids reported in [Violation.Rule].
const (
	RuleNoCmdImports      = "no-cmd-imports"
	RuleNoChildImports    = "no-child-imports"
	RuleNoTestHelpers     = "no-test-helpers-in-non-test-code"
	RuleThirdPartyAllowed = "third-party-allowlist"
	RuleBanned            = "banned-import"
	RuleModuleAllowed     = "module-import-allowlist"
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

		if tree, ok := treeOf(rules.ModuleImports, rel); ok {
			for _, imp := range concat(p.Imports, p.TestImports, p.XTestImports) {
				impRel, internal := relativeOK(rules.Module, imp)
				if internal && !within(impRel, tree) && !slices.Contains(rules.ModuleImports[tree], impRel) {
					add(RuleModuleAllowed, rel, imp)
				}
			}
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

// treeOf returns the longest key of trees that rel is in.
func treeOf(trees map[string][]string, rel string) (string, bool) {
	best, found := "", false
	for tree := range trees {
		if within(rel, tree) && len(tree) >= len(best) {
			best, found = tree, true
		}
	}
	return best, found
}

// within reports whether rel is tree or a package under it.
func within(rel, tree string) bool {
	return rel == tree || strings.HasPrefix(rel, tree+"/")
}

// isTestHelper reports whether a module package is itself test support code
// (internal/testkit, or an xxxtest package directly under package xxx, such
// as pkg/tool/tooltest); such packages may import testing.
func isTestHelper(rel string) bool {
	return rel == "internal/testkit" || strings.HasPrefix(rel, "internal/testkit/") || isXXXTest(rel)
}

// isXXXTest reports whether the last path element is the parent's name plus
// "test", the convention of net/http/httptest and our own pkg/tool/tooltest.
// A package that merely ends in "test" (pkg/latest) does not count.
func isXXXTest(path string) bool {
	dir, last, ok := cutLast(path)
	if !ok {
		return false
	}
	_, parent, ok := cutLast(dir)
	if !ok {
		parent = dir
	}
	return last == parent+"test"
}

func cutLast(path string) (dir, last string, ok bool) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "", path, false
	}
	return path[:i], path[i+1:], true
}

func isTestHelperImport(module, imp string) bool {
	if imp == "testing" || strings.HasPrefix(imp, "testing/") {
		return true
	}
	if rel, ok := relativeOK(module, imp); ok {
		return isTestHelper(rel)
	}
	return isStdlib(imp) && isXXXTest(imp) // e.g. net/http/httptest
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
