package a2ui

import (
	"maps"
	"slices"
	"strings"
	"sync"
)

// The catalog: the basic catalog of A2UI v0.9 (catalogs/basic/catalog.json)
// without the components deferred until a renderer draws them. Each
// component's properties are the catalog's, with their types from
// common_types.json; TestCatalogMatchesSpec compares them with the spec's
// copy in testdata.
//
// The tables are written as text and read on first use: as Go map literals
// they cost the binary (and every image with the agent API) their
// initialization code.

// kind is the type of a property's value.
type kind int

const (
	kString        kind = iota + 1 // DynamicString
	kNumber                        // DynamicNumber
	kBool                          // DynamicBoolean
	kStringList                    // DynamicStringList
	kValue                         // DynamicValue: any literal, binding or call
	kURL                           // DynamicString; a literal must be a safe URL
	kDate                          // DynamicString; a literal must be a date, time or date-time
	kID                            // ComponentId: a reference to a component
	kChildren                      // ChildList: IDs, or a template {componentId, path}
	kEnum                          // a literal string of enum
	kLiteralBool                   // a literal boolean
	kLiteralString                 // a literal string
	kLiteralNumber                 // a literal number
	kLiteralInt                    // a literal integer, at least 0
	kAction                        // Action
	kChecks                        // a list of CheckRule
	kOptions                       // ChoicePicker options
	kIcon                          // an icon name, {svgPath} or a binding
	kAccessibility                 // AccessibilityAttributes
	kBoolList                      // a list of at least two DynamicBoolean (and, or)
	kAny                           // any JSON value (a literal argument)
)

// kinds are the kinds by their names in the tables.
var kinds = []string{
	kString: "string", kNumber: "number", kBool: "bool", kStringList: "stringList", kValue: "value",
	kURL: "url", kDate: "date", kID: "id", kChildren: "children", kEnum: "enum",
	kLiteralBool: "literalBool", kLiteralString: "literal", kLiteralNumber: "literalNumber", kLiteralInt: "literalInt",
	kAction: "action", kChecks: "checks", kOptions: "options", kIcon: "icon", kAccessibility: "accessibility",
	kBoolList: "boolList", kAny: "any",
}

// prop describes one property.
type prop struct {
	kind     kind
	required bool
	enum     []string
	oneOf    bool // one of the function's oneOf arguments
}

func p(k kind) prop   { return prop{kind: k} }
func req(k kind) prop { return prop{kind: k, required: true} }

// commonSpec are the properties of every component besides id and
// component: ComponentCommon's accessibility and the catalog's weight.
const commonSpec = "accessibility:accessibility weight:literalNumber"

// componentSpec is the catalog's components, one per line: the type, then
// each property as name:kind, "*" after the name when it is required, and
// "=a|b" after an enum's kind for its values.
const componentSpec = `
Text          text*:string variant:enum=h1|h2|h3|h4|h5|caption|body
Image         url*:url description:string fit:enum=contain|cover|fill|none|scaleDown variant:enum=icon|avatar|smallFeature|mediumFeature|largeFeature|header
Icon          name*:icon
Row           children*:children justify:enum=center|end|spaceAround|spaceBetween|spaceEvenly|start|stretch align:enum=start|center|end|stretch
Column        children*:children justify:enum=start|center|end|spaceBetween|spaceAround|spaceEvenly|stretch align:enum=start|center|end|stretch
List          children*:children direction:enum=vertical|horizontal align:enum=start|center|end|stretch
Card          child*:id
Divider       axis:enum=horizontal|vertical
Button        child*:id variant:enum=default|primary|borderless action*:action checks:checks
TextField     label*:string value:string variant:enum=longText|number|shortText|obscured validationRegexp:literal checks:checks
CheckBox      label*:string value*:bool checks:checks
ChoicePicker  label:string variant:enum=multipleSelection|mutuallyExclusive options*:options value*:stringList displayStyle:enum=checkbox|chips filterable:literalBool checks:checks
DateTimeInput value*:string enableDate:literalBool enableTime:literalBool min:date max:date label:string checks:checks
`

// functionSpec is the catalog's functions, one per line: the name and what
// it returns, then its arguments as in componentSpec, "+" after the name of
// arguments of which at least one is required.
const functionSpec = `
required:boolean        value*:value
regex:boolean           value*:string pattern*:literal
length:boolean          value*:string min+:literalInt max+:literalInt
numeric:boolean         value*:number min+:literalNumber max+:literalNumber
email:boolean           value*:string
formatString:string     value*:string
formatNumber:string     value*:number decimals:number grouping:bool
formatCurrency:string   value*:number currency*:string decimals:number grouping:bool
formatDate:string       value*:value format*:string
pluralize:string        value*:number zero:string one:string two:string few:string many:string other*:string
openUrl:void            url*:url
and:boolean             values*:boolList
or:boolean              values*:boolList
not:boolean             value*:bool
`

// themeSpec is the catalog's theme parameters.
const themeSpec = "primaryColor:literal iconUrl:url agentDisplayName:literal"

// fn describes one of the catalog's functions: its arguments and what it
// returns.
type fn struct {
	args    map[string]prop
	returns string
	// oneOf, when set, names arguments of which at least one is required.
	oneOf []string
}

// tables are the catalog, read from the specs above.
type tables struct {
	components map[string]map[string]prop
	functions  map[string]fn
	theme      map[string]prop
}

// catalog returns the tables, read on first use. It is a function rather
// than a sync.OnceValue variable so that a binary that uses none of the
// validator (the agent API alone) links none of this.
func catalog() tables {
	catalogOnce.Do(func() { catalogTables = readTables() })
	return catalogTables
}

var (
	catalogOnce   sync.Once
	catalogTables tables
)

// readTables reads the tables from the specs above.
func readTables() tables {
	t := tables{components: map[string]map[string]prop{}, functions: map[string]fn{}, theme: parseProps(themeSpec)}
	for _, line := range lines(componentSpec) {
		name, rest, _ := strings.Cut(line, " ")
		ps := parseProps(rest)
		maps.Copy(ps, parseProps(commonSpec))
		t.components[name] = ps
	}
	for _, line := range lines(functionSpec) {
		head, rest, _ := strings.Cut(line, " ")
		name, returns, _ := strings.Cut(head, ":")
		f := fn{args: parseProps(rest), returns: returns}
		for _, a := range slices.Sorted(maps.Keys(f.args)) {
			if f.args[a].oneOf {
				f.oneOf = append(f.oneOf, a)
			}
		}
		t.functions[name] = f
	}
	return t
}

func lines(spec string) []string {
	var out []string
	for _, l := range strings.Split(spec, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseProps reads "name[*|+]:kind[=a|b] ..."; a malformed table panics, on
// first use (every test uses it).
func parseProps(spec string) map[string]prop {
	out := map[string]prop{}
	for _, field := range strings.Fields(spec) {
		name, k, _ := strings.Cut(field, ":")
		k, values, isEnum := strings.Cut(k, "=")
		var pr prop
		if pr.kind = kind(slices.Index(kinds, k)); pr.kind <= 0 || name == "" {
			panic("a2ui: bad catalog field " + field)
		}
		if isEnum {
			pr.enum = strings.Split(values, "|")
		}
		switch {
		case strings.HasSuffix(name, "*"):
			name, pr.required = strings.TrimSuffix(name, "*"), true
		case strings.HasSuffix(name, "+"):
			name, pr.oneOf = strings.TrimSuffix(name, "+"), true
		}
		out[name] = pr
	}
	return out
}

// deferred are the basic catalog's components left out until a renderer
// draws them.
var deferred = []string{"AudioPlayer", "Modal", "Slider", "Tabs", "Video"}

// Components returns the component types of the catalog, sorted.
func Components() []string { return slices.Sorted(maps.Keys(catalog().components)) }

// Deferred returns the basic catalog's component types this catalog leaves
// out, sorted: a surface that uses one is rejected.
func Deferred() []string { return slices.Clone(deferred) }

// returnTypes are the values of a function call's returnType.
var returnTypes = []string{"string", "number", "boolean", "array", "object", "any", "void"}

// icons are the basic catalog's icon names.
var icons = []string{
	"accountCircle", "add", "arrowBack", "arrowForward", "attachFile", "calendarToday", "call", "camera", "check", "close",
	"delete", "download", "edit", "event", "error", "fastForward", "favorite", "favoriteOff", "folder", "help", "home", "info",
	"locationOn", "lock", "lockOpen", "mail", "menu", "moreVert", "moreHoriz", "notificationsOff", "notifications", "pause",
	"payment", "person", "phone", "photo", "play", "print", "refresh", "rewind", "search", "send", "settings", "share",
	"shoppingCart", "skipNext", "skipPrevious", "star", "starHalf", "starOff", "stop", "upload", "visibility",
	"visibilityOff", "volumeDown", "volumeMute", "volumeOff", "volumeUp", "warning",
}
