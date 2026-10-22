package a2ui

import (
	"maps"
	"slices"
)

// TestCatalog is the catalog's tables as names, for the test that compares
// them with the spec's.
type TestCatalog struct {
	Components    map[string]TestProps
	Functions     map[string]TestProps
	FunctionNames []string
	Checkable     []string // components with checks
}

// TestProps are an object's properties, its required ones and its enums,
// sorted.
type TestProps struct {
	Props, Required []string
	Enums           map[string][]string
}

func testProps(spec map[string]prop, extra ...string) TestProps {
	out := TestProps{Props: slices.Sorted(slices.Values(append(slices.Collect(maps.Keys(spec)), extra...))), Required: slices.Clone(extra), Enums: map[string][]string{}}
	for name, p := range spec {
		if p.required {
			out.Required = append(out.Required, name)
		}
		if len(p.enum) > 0 {
			out.Enums[name] = slices.Sorted(slices.Values(p.enum))
		}
	}
	slices.Sort(out.Required)
	return out
}

// CatalogForTest returns the catalog's tables.
func CatalogForTest() TestCatalog {
	c := TestCatalog{Components: map[string]TestProps{}, Functions: map[string]TestProps{}}
	for name, spec := range catalog().components {
		c.Components[name] = testProps(spec, "id")
		if _, ok := spec["checks"]; ok {
			c.Checkable = append(c.Checkable, name)
		}
	}
	for name, f := range catalog().functions {
		c.Functions[name] = testProps(f.args)
		c.FunctionNames = append(c.FunctionNames, name)
	}
	return c
}
