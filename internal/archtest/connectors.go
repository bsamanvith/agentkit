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
)

// kitPath is the connector test kit, relative to the module.
const kitPath = "pkg/toolset/toolsettest"

// CheckConnectors checks every connector: each package directly under
// root/connectors. Its tests must call the kit's RunConnector and
// FuzzResponses, and testdata/cassettes must hold at least one recorded
// request (.req.json). A connector cannot opt out of the kit by forgetting it.
func CheckConnectors(root, module string) ([]Violation, error) {
	entries, err := os.ReadDir(filepath.Join(root, "connectors"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") || e.Name() == "testdata" {
			continue
		}
		rel := path.Join("connectors", e.Name())
		dir := filepath.Join(root, "connectors", e.Name())
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(files, func(f string) bool { return !strings.HasSuffix(f, "_test.go") }) {
			continue // a directory of subpackages, not a connector
		}
		calls, err := kitCalls(files, module+"/"+kitPath)
		if err != nil {
			return nil, err
		}
		if !calls["RunConnector"] {
			out = append(out, Violation{Rule: RuleConnectorSuite, Package: rel, Detail: "has no test that calls toolsettest.RunConnector"})
		}
		if !calls["FuzzResponses"] {
			out = append(out, Violation{Rule: RuleConnectorFuzz, Package: rel, Detail: "has no fuzz target that calls toolsettest.FuzzResponses"})
		}
		reqs, err := filepath.Glob(filepath.Join(dir, "testdata", "cassettes", "*.req.json"))
		if err != nil {
			return nil, err
		}
		if len(reqs) == 0 {
			out = append(out, Violation{Rule: RuleConnectorCassettes, Package: rel, Detail: "has no cassette in testdata/cassettes (make record-cassettes)"})
		}
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
