package archtest

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Connector rule ids reported in [Violation.Rule] (docs/CONNECTORS.md).
const (
	RuleConnectorSuite     = "connector-runs-the-suite"
	RuleConnectorFuzz      = "connector-fuzzes-responses"
	RuleConnectorCassettes = "connector-has-cassettes"
	RuleConnectorPending   = "connector-pending-is-stale"
)

// kitPath is the connector test kit, relative to the module.
const kitPath = "pkg/toolset/toolsettest"

// CheckConnectors checks every connector: each package under root/connectors,
// at any depth (a family such as connectors/google holds one package per
// toolset). Its tests must call the kit's RunConnector and FuzzResponses, and
// testdata/cassettes must hold at least one recorded request (.req.json). A
// connector cannot opt out of the kit by forgetting it. A connector in
// rules.PendingConnectors is exempt until it passes, and then its entry must
// go. A package in rules.ConnectorLibraries is shared code for connectors
// (such as a family's HTTP client), not a connector, and is not checked.
func CheckConnectors(root string, rules Rules) ([]Violation, error) {
	module := rules.Module
	base := filepath.Join(root, "connectors")
	if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var out []Violation
	err := filepath.WalkDir(base, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || dir == base {
			return nil
		}
		if name := d.Name(); strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
			return filepath.SkipDir
		}
		relDir, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relDir)
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(files, func(f string) bool { return !strings.HasSuffix(f, "_test.go") }) {
			return nil // a directory of subpackages, not a connector
		}
		if slices.Contains(rules.ConnectorLibraries, rel) {
			return nil
		}
		var vs []Violation
		calls, err := kitCalls(files, module+"/"+kitPath)
		if err != nil {
			return err
		}
		if !calls["RunConnector"] {
			vs = append(vs, Violation{Rule: RuleConnectorSuite, Package: rel, Detail: "has no test that calls toolsettest.RunConnector"})
		}
		if !calls["FuzzResponses"] {
			vs = append(vs, Violation{Rule: RuleConnectorFuzz, Package: rel, Detail: "has no fuzz target that calls toolsettest.FuzzResponses"})
		}
		reqs, err := filepath.Glob(filepath.Join(dir, "testdata", "cassettes", "*.req.json"))
		if err != nil {
			return err
		}
		if len(reqs) == 0 {
			vs = append(vs, Violation{Rule: RuleConnectorCassettes, Package: rel, Detail: "has no cassette in testdata/cassettes (make record-cassettes)"})
		}
		_, pending := rules.PendingConnectors[rel]
		switch {
		case pending && len(vs) == 0:
			out = append(out, Violation{Rule: RuleConnectorPending, Package: rel, Detail: "passes the connector rules: remove it from PendingConnectors"})
		case !pending:
			out = append(out, vs...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// kitCalls returns the names of the kit's functions the test files among
// files call, through whatever name they import the kit as.
func kitCalls(files []string, kit string) (map[string]bool, error) {
	called := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p != kit {
				continue
			}
			name := path.Base(kit)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && slices.Contains(names, x.Name) {
					called[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	return called, nil
}
