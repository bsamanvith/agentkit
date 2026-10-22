package a2ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxProblems is the most problems one validation reports, and maxNesting
// the deepest a message's JSON may nest.
const (
	maxProblems = 10
	maxNesting  = MaxDepth + 16
)

// nests is how deep v nests below depth d, stopping past maxNesting.
func nests(v any, d int) int {
	if d > maxNesting {
		return d
	}
	deepest := d
	switch v := v.(type) {
	case map[string]any:
		for _, item := range v {
			deepest = max(deepest, nests(item, d+1))
		}
	case []any:
		for _, item := range v {
			deepest = max(deepest, nests(item, d+1))
		}
	}
	return deepest
}

// Parse decodes and validates one message ([Validate]).
func Parse(data []byte) (Message, error) {
	msgs, err := parse([][]byte{data}, func(int) string { return "" })
	if err != nil {
		return Message{}, err
	}
	return msgs[0], nil
}

// ParseInput decodes and validates a ui_render call's input,
// {"messages": [message, ...]}, as one batch ([ValidateBatch]). Problems'
// paths are pointers into the input ("/messages/1/updateComponents/...").
func ParseInput(input []byte) ([]Message, error) {
	if len(input) > MaxInputBytes {
		return nil, Errors{{Path: "", Err: ErrTooLarge, Detail: fmt.Sprintf("the input is %d bytes, over the limit of %d: split the UI into fewer, smaller surfaces", len(input), MaxInputBytes)}}
	}
	v, err := decode(input)
	if err != nil {
		return nil, Errors{{Path: "", Err: ErrInvalid, Detail: "the input is not JSON: " + err.Error()}}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, Errors{{Path: "", Err: ErrInvalid, Detail: `the input must be an object: {"messages": [...]}`}}
	}
	var c checker
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if k != "messages" {
			c.fail("/"+escape(k), ErrUnknownProperty, "unknown property %q: the input has only \"messages\"", k)
		}
	}
	list, ok := obj["messages"].([]any)
	switch {
	case obj["messages"] == nil:
		c.fail("/messages", ErrInvalid, "missing \"messages\": a list of A2UI messages")
	case !ok:
		c.fail("/messages", ErrInvalid, "\"messages\" must be a list of A2UI messages")
	case len(list) == 0:
		c.fail("/messages", ErrInvalid, "\"messages\" is empty")
	case len(list) > MaxMessages:
		c.fail("/messages", ErrTooLarge, "%d messages, over the limit of %d", len(list), MaxMessages)
	}
	if len(c.errs) > 0 {
		return nil, c.errs
	}
	raws := make([][]byte, len(list))
	for i, m := range list {
		raws[i], _ = json.Marshal(m) // decoded from JSON: it encodes
	}
	return parse(raws, func(i int) string { return "/messages/" + strconv.Itoa(i) })
}

// parse validates the messages as one batch and decodes them.
func parse(raws [][]byte, prefix func(int) string) ([]Message, error) {
	vs := make([]any, len(raws))
	for i, raw := range raws {
		v, err := decode(raw)
		if err != nil {
			return nil, Errors{{Path: prefix(i), Err: ErrInvalid, Detail: "the message is not JSON: " + err.Error()}}
		}
		vs[i] = v
	}
	if err := validate(vs, raws, prefix); err != nil {
		return nil, err
	}
	out := make([]Message, len(vs))
	for i, raw := range raws {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&out[i]); err != nil { // valid: it fits the types
			return nil, Errors{{Path: prefix(i), Err: ErrInvalid, Detail: err.Error()}}
		}
	}
	return out, nil
}

// Validate checks one message: its envelope, its components against the
// catalog, their references (a surface's components define its whole tree,
// so each child they name is among them, and they hold a root and no
// cycle), and the limits. The error, if any, is [Errors].
func Validate(m Message) error { return ValidateBatch([]Message{m}) }

// ValidateBatch checks the messages of one ui_render call: each as
// [Validate] does, except that a surface's components may be spread over
// the call's updateComponents messages (a later definition of an ID replaces
// an earlier one), and that the messages' order must make sense: a surface
// is created at most once, before its updates, and nothing follows its
// deletion but a new createSurface. Problems' paths start with the
// message's index ("/1/updateComponents/...").
func ValidateBatch(msgs []Message) error {
	if len(msgs) > MaxMessages {
		return Errors{{Err: ErrTooLarge, Detail: fmt.Sprintf("%d messages, over the limit of %d", len(msgs), MaxMessages)}}
	}
	raws := make([][]byte, len(msgs))
	vs := make([]any, len(msgs))
	prefix := func(i int) string { return "" }
	if len(msgs) > 1 {
		prefix = func(i int) string { return "/" + strconv.Itoa(i) }
	}
	for i, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return Errors{{Path: prefix(i), Err: ErrInvalid, Detail: "the message does not encode as JSON: " + err.Error()}}
		}
		if raws[i] = raw; len(raw) <= MaxMessageBytes {
			if vs[i], err = decode(raw); err != nil {
				return Errors{{Path: prefix(i), Err: ErrInvalid, Detail: err.Error()}}
			}
		}
	}
	return validate(vs, raws, prefix)
}

// decode reads JSON into any, numbers as json.Number, refusing trailing
// data.
func decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

// checker collects problems.
type checker struct {
	errs Errors
}

func (c *checker) fail(path string, sentinel error, format string, args ...any) {
	if len(c.errs) >= maxProblems {
		return
	}
	c.errs = append(c.errs, &ValidationError{Path: path, Err: sentinel, Detail: fmt.Sprintf(format, args...)})
}

func (c *checker) full() bool { return len(c.errs) >= maxProblems }

// surface is what a batch says about one surface's components.
type surface struct {
	defs  map[string]def // by ID, the last definition
	first string         // path of the first updateComponents
}

// def is one component definition: where it is, its type, and the
// components it refers to.
type def struct {
	path string
	refs []ref
}

// ref is a reference to a component, at path.
type ref struct {
	path, id string
}

// validate checks decoded messages (vs, from raws) as one batch.
func validate(vs []any, raws [][]byte, prefix func(int) string) error {
	var c checker
	surfaces := map[string]*surface{}
	var order []string           // surfaces with components, in the order seen
	state := map[string]string{} // surface → its last lifecycle message: created, deleted, updated
	for i, v := range vs {
		at := prefix(i)
		if len(raws[i]) > MaxMessageBytes {
			c.fail(at, ErrTooLarge, "the message is %d bytes, over the limit of %d: split it, or move long data out", len(raws[i]), MaxMessageBytes)
			continue
		}
		if nests(v, 0) > maxNesting {
			c.fail(at, ErrTooLarge, "the message nests deeper than %d", maxNesting)
			continue
		}
		kind, id, comps := c.message(at, v)
		if c.full() {
			break
		}
		if id == "" {
			continue
		}
		switch prev := state[id]; {
		case kind == kindCreate && (prev == "created" || prev == "updated"):
			c.fail(at+"/"+kind, ErrInvalid, "surface %q is created twice, or after its updates, in this call: send createSurface once, first", id)
		case kind == kindCreate:
			state[id] = "created"
		case kind == kindDelete:
			state[id] = "deleted"
		case prev == "deleted":
			c.fail(at+"/"+kind, ErrInvalid, "surface %q is deleted earlier in this call: create it again first", id)
		default:
			state[id] = "updated"
		}
		if kind != kindUpdate || comps == nil { // no list to check the tree of
			continue
		}
		s := surfaces[id]
		if s == nil {
			s = &surface{defs: map[string]def{}, first: at + "/" + kind}
			surfaces[id] = s
			order = append(order, id)
		}
		maps.Copy(s.defs, comps)
	}
	if !c.full() {
		for _, id := range order {
			c.tree(id, surfaces[id])
		}
	}
	if len(c.errs) > 0 {
		return c.errs
	}
	return nil
}

// message checks one message's envelope and body. It returns the message's
// kind, its surface's ID and, for updateComponents, its definitions.
func (c *checker) message(at string, v any) (kind, surfaceID string, defs map[string]def) {
	obj, ok := v.(map[string]any)
	if !ok {
		c.fail(at, ErrInvalid, `a message must be an object: {"version": %q, "createSurface" | "updateComponents" | "updateDataModel" | "deleteSurface": {...}}`, Version)
		return "", "", nil
	}
	var kinds []string
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		switch k {
		case "version":
		case kindCreate, kindUpdate, kindData, kindDelete:
			kinds = append(kinds, k)
		default:
			c.fail(at+"/"+escape(k), ErrUnknownProperty, "unknown property %q: a message has \"version\" and one of createSurface, updateComponents, updateDataModel, deleteSurface", k)
		}
	}
	if s, ok := obj["version"].(string); !ok || s != Version {
		c.fail(at+"/version", ErrInvalid, "\"version\" must be %q", Version)
	}
	switch len(kinds) {
	case 0:
		c.fail(at, ErrInvalid, "the message has none of createSurface, updateComponents, updateDataModel, deleteSurface")
		return "", "", nil
	case 1:
	default:
		c.fail(at, ErrInvalid, "the message has %s: send each as its own message", strings.Join(kinds, " and "))
		return "", "", nil
	}
	kind = kinds[0]
	at += "/" + kind
	body, ok := obj[kind].(map[string]any)
	if !ok {
		c.fail(at, ErrInvalid, "%s must be an object", kind)
		return kind, "", nil
	}
	allowed := map[string][]string{
		kindCreate: {"surfaceId", "catalogId", "theme", "sendDataModel"},
		kindUpdate: {"surfaceId", "components"},
		kindData:   {"surfaceId", "path", "value"},
		kindDelete: {"surfaceId"},
	}[kind]
	for _, k := range slices.Sorted(maps.Keys(body)) {
		if !slices.Contains(allowed, k) {
			c.fail(at+"/"+escape(k), ErrUnknownProperty, "unknown property %q of %s: it has %s", k, kind, strings.Join(allowed, ", "))
		}
	}
	surfaceID, _ = body["surfaceId"].(string)
	if !c.id(at+"/surfaceId", body["surfaceId"], "surfaceId") {
		surfaceID = ""
	}
	switch kind {
	case kindCreate:
		switch cat, ok := body["catalogId"].(string); {
		case body["catalogId"] == nil:
			c.fail(at+"/catalogId", ErrInvalid, "missing catalogId: use %q", BasicCatalogID)
		case !ok || cat != BasicCatalogID:
			c.fail(at+"/catalogId", ErrUnknownCatalog, "catalogId must be %q, the only catalog here", BasicCatalogID)
		}
		if t, ok := body["theme"]; ok {
			c.object(at+"/theme", t, catalog().theme, "theme")
			if color, ok := asMap(t)["primaryColor"].(string); ok && !hexColor.MatchString(color) {
				c.fail(at+"/theme/primaryColor", ErrInvalid, "primaryColor must be a color like \"#00BFFF\"")
			}
		}
		if v, ok := body["sendDataModel"]; ok {
			if _, ok := v.(bool); !ok {
				c.fail(at+"/sendDataModel", ErrInvalid, "sendDataModel must be true or false")
			}
		}
	case kindUpdate:
		defs = c.components(at+"/components", body["components"])
	case kindData:
		if v, ok := body["path"]; ok {
			if s, isStr := v.(string); isStr {
				c.pointer(at+"/path", s, true)
			} else {
				c.fail(at+"/path", ErrInvalid, "path must be a JSON pointer such as \"/user/name\"")
			}
		}
		if v, ok := body["value"]; ok {
			c.depth(at+"/value", v, 0)
		}
	}
	return kind, surfaceID, defs
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// components checks a component list and returns its definitions.
func (c *checker) components(at string, v any) map[string]def {
	list, ok := v.([]any)
	switch {
	case v == nil:
		c.fail(at, ErrInvalid, "missing components: a list of the surface's components")
		return nil
	case !ok:
		c.fail(at, ErrInvalid, "components must be a list")
		return nil
	case len(list) == 0:
		c.fail(at, ErrInvalid, "components is empty")
		return nil
	case len(list) > MaxComponents:
		c.fail(at, ErrTooLarge, "%d components, over the limit of %d", len(list), MaxComponents)
		return nil
	}
	defs := map[string]def{}
	for i, item := range list {
		p := at + "/" + strconv.Itoa(i)
		id, d, ok := c.component(p, item)
		if !ok {
			continue
		}
		if prev, dup := defs[id]; dup {
			c.fail(p+"/id", ErrInvalid, "id %q is used twice in this list (also at %s): IDs are unique", id, prev.path)
			continue
		}
		defs[id] = d
	}
	return defs
}

// component checks one component. ok is false when it has no usable ID.
func (c *checker) component(at string, v any) (id string, d def, ok bool) {
	obj, isObj := v.(map[string]any)
	if !isObj {
		c.fail(at, ErrInvalid, `a component must be an object: {"id": ..., "component": ..., properties}`)
		return "", def{}, false
	}
	d.path = at
	ok = c.id(at+"/id", obj["id"], "id")
	id, _ = obj["id"].(string)
	typ, isStr := obj["component"].(string)
	spec, known := catalog().components[typ]
	switch {
	case obj["component"] == nil:
		c.fail(at+"/component", ErrInvalid, "missing \"component\", the component's type: one of %s", strings.Join(Components(), ", "))
		return id, d, ok
	case !isStr:
		c.fail(at+"/component", ErrInvalid, "\"component\" must be a type name: one of %s", strings.Join(Components(), ", "))
		return id, d, ok
	case slices.Contains(deferred, typ):
		c.fail(at+"/component", ErrUnknownComponent, "component %q is not in this catalog yet; use one of %s", typ, strings.Join(Components(), ", "))
		return id, d, ok
	case !known:
		c.fail(at+"/component", ErrUnknownComponent, "unknown component %q; use one of %s", typ, strings.Join(Components(), ", "))
		return id, d, ok
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if k == "id" || k == "component" {
			continue
		}
		pr, has := spec[k]
		if !has {
			c.fail(at+"/"+escape(k), ErrUnknownProperty, "%s has no property %q; it has %s", typ, k, strings.Join(names(spec), ", "))
			continue
		}
		d.refs = append(d.refs, c.value(at+"/"+escape(k), obj[k], pr)...)
	}
	for _, k := range names(spec) {
		if _, has := obj[k]; !has && spec[k].required {
			c.fail(at, ErrInvalid, "%s %q is missing its required property %q", typ, id, k)
		}
	}
	return id, d, ok
}

// names are a property set's names, sorted.
func names(spec map[string]prop) []string { return slices.Sorted(maps.Keys(spec)) }

// object checks an object whose properties are spec's, and returns the
// references it holds.
func (c *checker) object(at string, v any, spec map[string]prop, what string) []ref {
	obj, ok := v.(map[string]any)
	if !ok {
		c.fail(at, ErrInvalid, "%s must be an object", what)
		return nil
	}
	var refs []ref
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		pr, has := spec[k]
		if !has {
			c.fail(at+"/"+escape(k), ErrUnknownProperty, "%s has no property %q; it has %s", what, k, strings.Join(names(spec), ", "))
			continue
		}
		refs = append(refs, c.value(at+"/"+escape(k), obj[k], pr)...)
	}
	for _, k := range names(spec) {
		if _, has := obj[k]; !has && spec[k].required {
			c.fail(at, ErrInvalid, "%s is missing its required property %q", what, k)
		}
	}
	return refs
}

// value checks a property's value and returns the references it holds.
func (c *checker) value(at string, v any, pr prop) []ref {
	switch pr.kind {
	case kString, kURL, kDate:
		c.dynamic(at, v, "string", pr.kind)
	case kNumber:
		c.dynamic(at, v, "number", pr.kind)
	case kBool:
		c.dynamic(at, v, "boolean", pr.kind)
	case kStringList:
		c.dynamic(at, v, "array", pr.kind)
	case kValue:
		c.dynamic(at, v, "any", pr.kind)
	case kID:
		if c.id(at, v, "a component ID") {
			return []ref{{at, v.(string)}}
		}
	case kChildren:
		return c.children(at, v)
	case kEnum:
		if s, ok := v.(string); !ok || !slices.Contains(pr.enum, s) {
			c.fail(at, ErrInvalid, "must be one of %s", strings.Join(pr.enum, ", "))
		}
	case kLiteralBool:
		if _, ok := v.(bool); !ok {
			c.fail(at, ErrInvalid, "must be true or false")
		}
	case kLiteralString:
		c.str(at, v)
	case kLiteralNumber:
		if _, ok := v.(json.Number); !ok {
			c.fail(at, ErrInvalid, "must be a number")
		}
	case kLiteralInt:
		n, ok := v.(json.Number)
		if i, err := strconv.ParseInt(string(n), 10, 64); !ok || err != nil || i < 0 {
			c.fail(at, ErrInvalid, "must be a whole number, at least 0")
		}
	case kAction:
		return c.action(at, v)
	case kChecks:
		c.checks(at, v)
	case kOptions:
		c.options(at, v)
	case kIcon:
		c.icon(at, v)
	case kAccessibility:
		c.object(at, v, map[string]prop{"label": p(kString), "description": p(kString)}, "accessibility")
	case kBoolList:
		list, ok := v.([]any)
		if !ok || len(list) < 2 {
			c.fail(at, ErrInvalid, "must be a list of at least two booleans, bindings or calls")
			return nil
		}
		for i, item := range list {
			c.dynamic(at+"/"+strconv.Itoa(i), item, "boolean", kBool)
		}
	case kAny:
		c.depth(at, v, 0)
	}
	return nil
}

// dynamic checks a dynamic value: a literal of type want ("any" for every
// literal), a data binding {"path"}, or a function call {"call", "args"}
// returning want. k refines a literal string: a URL or a date.
func (c *checker) dynamic(at string, v any, want string, k kind) {
	if obj, ok := v.(map[string]any); ok {
		if _, isCall := obj["call"]; isCall {
			c.call(at, obj, want, false)
			return
		}
		c.binding(at, obj)
		return
	}
	ok := false
	switch v := v.(type) {
	case string:
		ok = want == "string" || want == "any"
		if ok {
			c.str(at, v)
			switch {
			case k == kURL:
				c.url(at, v)
			case k == kDate && !isDate(v):
				c.fail(at, ErrInvalid, "must be a date (2026-02-10), a time (15:04) or a date and time (2026-02-10T15:04:00Z)")
			}
		}
	case json.Number:
		ok = want == "number" || want == "any"
	case bool:
		ok = want == "boolean" || want == "any"
	case []any:
		ok = want == "array" || want == "any"
		if ok {
			if want == "array" {
				for i, item := range v {
					if _, isStr := item.(string); !isStr {
						c.fail(at+"/"+strconv.Itoa(i), ErrInvalid, "must be a string")
					} else {
						c.str(at+"/"+strconv.Itoa(i), item)
					}
				}
			} else {
				c.depth(at, v, 0)
			}
		}
	}
	if !ok {
		what := map[string]string{"string": "a string", "number": "a number", "boolean": "true or false", "array": "a list of strings", "any": "a value"}[want]
		c.fail(at, ErrInvalid, "must be %s, a data binding {\"path\": \"/...\"} or a function call {\"call\": ...}", what)
	}
}

// binding checks a data binding {"path": pointer}.
func (c *checker) binding(at string, obj map[string]any) {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if k != "path" {
			c.fail(at+"/"+escape(k), ErrUnknownProperty, "a data binding has only \"path\" (a function call has \"call\" and \"args\"), not %q", k)
		}
	}
	s, ok := obj["path"].(string)
	if !ok {
		c.fail(at+"/path", ErrInvalid, "a data binding's path must be a JSON pointer such as \"/user/name\"")
		return
	}
	c.pointer(at+"/path", s, false)
}

// pointer checks a data model path: a JSON pointer ("/a/b", "" or "/" for
// the whole model) or, unless absolute, a path relative to a template's
// item ("name"). It reports whether it is one.
func (c *checker) pointer(at, s string, absolute bool) bool {
	if !c.str(at, s) {
		return false
	}
	if absolute && s != "" && !strings.HasPrefix(s, "/") {
		c.fail(at, ErrInvalid, "the path must start with \"/\"")
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' && (i+1 >= len(s) || (s[i+1] != '0' && s[i+1] != '1')) {
			c.fail(at, ErrInvalid, "\"~\" in a JSON pointer must be written ~0 (and \"/\" in a key ~1)")
			return false
		}
	}
	return true
}

// call checks a function call returning want. A check written as its
// condition's call (see checks) has a message too.
func (c *checker) call(at string, obj map[string]any, want string, check bool) {
	name, _ := obj["call"].(string)
	f, known := catalog().functions[name]
	if !known {
		c.fail(at+"/call", ErrInvalid, "unknown function %v; the catalog has %s", jsonText(obj["call"]), strings.Join(slices.Sorted(maps.Keys(catalog().functions)), ", "))
		return
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if k != "call" && k != "args" && k != "returnType" && (!check || k != "message") {
			c.fail(at+"/"+escape(k), ErrUnknownProperty, "a function call has call, args and returnType, not %q", k)
		}
	}
	if rt, ok := obj["returnType"]; ok {
		s, isStr := rt.(string)
		switch {
		case !isStr || !slices.Contains(returnTypes, s):
			c.fail(at+"/returnType", ErrInvalid, "returnType must be one of %s", strings.Join(returnTypes, ", "))
		case s != f.returns && s != "any":
			c.fail(at+"/returnType", ErrInvalid, "%s returns %s, not %s", name, f.returns, s)
		}
	}
	if want != "any" && f.returns != want {
		c.fail(at+"/call", ErrInvalid, "%s returns %s, but %s is wanted here", name, f.returns, want)
	}
	args, ok := obj["args"]
	if !ok {
		args = map[string]any{}
	}
	c.object(at+"/args", args, f.args, name+"'s args")
	if len(f.oneOf) > 0 {
		am := asMap(args)
		if !slices.ContainsFunc(f.oneOf, func(k string) bool { _, has := am[k]; return has }) {
			c.fail(at+"/args", ErrInvalid, "%s needs %s", name, strings.Join(f.oneOf, " or "))
		}
	}
}

// children checks a ChildList: a list of IDs, or a template.
func (c *checker) children(at string, v any) []ref {
	switch v := v.(type) {
	case []any:
		if len(v) > MaxChildren {
			c.fail(at, ErrTooLarge, "%d children, over the limit of %d", len(v), MaxChildren)
			return nil
		}
		var refs []ref
		for i, item := range v {
			p := at + "/" + strconv.Itoa(i)
			if c.id(p, item, "a child's component ID") {
				refs = append(refs, ref{p, item.(string)})
			}
		}
		return refs
	case map[string]any:
		return c.object(at, v, map[string]prop{"componentId": req(kID), "path": req(kLiteralString)}, "a child template")
	}
	c.fail(at, ErrInvalid, "children must be a list of component IDs, or a template {\"componentId\": ..., \"path\": \"/list\"}")
	return nil
}

// action checks a component's action: a server event or a local call.
func (c *checker) action(at string, v any) []ref {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != 1 || (obj["event"] == nil && obj["functionCall"] == nil) {
		c.fail(at, ErrInvalid, `an action is {"event": {"name": ..., "context": {...}}} or {"functionCall": {"call": ...}}`)
		return nil
	}
	if ev, ok := obj["event"]; ok {
		e, isMap := ev.(map[string]any)
		if !isMap {
			c.fail(at+"/event", ErrInvalid, `an action's event is {"name": ..., "context": {...}}`)
			return nil
		}
		for _, k := range slices.Sorted(maps.Keys(e)) {
			if k != "name" && k != "context" {
				c.fail(at+"/event/"+escape(k), ErrUnknownProperty, "an action's event has name and context, not %q", k)
			}
		}
		if n, ok := e["name"].(string); !ok || strings.TrimSpace(n) == "" {
			c.fail(at+"/event/name", ErrInvalid, "the event needs a name: what the agent is told the person did")
		} else {
			c.str(at+"/event/name", n)
		}
		if ctx, ok := e["context"]; ok {
			m, isMap := ctx.(map[string]any)
			if !isMap {
				c.fail(at+"/event/context", ErrInvalid, "the event's context must be an object of values, bindings and calls")
				return nil
			}
			for _, k := range slices.Sorted(maps.Keys(m)) {
				c.dynamic(at+"/event/context/"+escape(k), m[k], "any", kValue)
			}
		}
		return nil
	}
	fc, isMap := obj["functionCall"].(map[string]any)
	if !isMap {
		c.fail(at+"/functionCall", ErrInvalid, "functionCall must be a function call {\"call\": ..., \"args\": {...}}")
		return nil
	}
	c.call(at+"/functionCall", fc, "any", false)
	return nil
}

// checks checks a component's checks: a list of {"condition", "message"}.
// The spec's own example writes a check as the condition's call with a
// message ({"call", "args", "message"}), which is taken too.
func (c *checker) checks(at string, v any) {
	list, ok := v.([]any)
	if !ok {
		c.fail(at, ErrInvalid, `checks must be a list of {"condition": ..., "message": "..."}`)
		return
	}
	for i, item := range list {
		p := at + "/" + strconv.Itoa(i)
		obj, ok := item.(map[string]any)
		if !ok {
			c.fail(p, ErrInvalid, `a check is {"condition": ..., "message": "..."}`)
			continue
		}
		if _, short := obj["call"]; short {
			c.call(p, obj, "boolean", true)
			if _, ok := obj["message"].(string); !ok {
				c.fail(p, ErrInvalid, "a check needs a message")
			}
			continue
		}
		c.object(p, obj, map[string]prop{"condition": req(kBool), "message": req(kLiteralString)}, "a check")
	}
}

// options checks a ChoicePicker's options.
func (c *checker) options(at string, v any) {
	list, ok := v.([]any)
	switch {
	case !ok:
		c.fail(at, ErrInvalid, `options must be a list of {"label": ..., "value": "..."}`)
		return
	case len(list) > MaxOptions:
		c.fail(at, ErrTooLarge, "%d options, over the limit of %d", len(list), MaxOptions)
		return
	}
	for i, item := range list {
		c.object(at+"/"+strconv.Itoa(i), item, map[string]prop{"label": req(kString), "value": req(kLiteralString)}, "an option")
	}
}

// icon checks an Icon's name: a name of the catalog, {"svgPath"} or a
// binding.
func (c *checker) icon(at string, v any) {
	switch v := v.(type) {
	case string:
		if !slices.Contains(icons, v) {
			c.fail(at, ErrInvalid, "unknown icon %q; the catalog has %s", v, strings.Join(icons, ", "))
		}
		return
	case map[string]any:
		if _, ok := v["svgPath"]; ok {
			c.object(at, v, map[string]prop{"svgPath": req(kLiteralString)}, "an icon")
			return
		}
		c.binding(at, v)
		return
	}
	c.fail(at, ErrInvalid, "an icon is a name (such as \"search\"), {\"svgPath\": ...} or a data binding")
}

// id checks an ID: a non-empty string of printable characters, within the
// limit.
func (c *checker) id(at string, v any, what string) bool {
	s, ok := v.(string)
	switch {
	case v == nil:
		c.fail(at, ErrInvalid, "missing %s", what)
	case !ok:
		c.fail(at, ErrInvalid, "%s must be a string", what)
	case s == "":
		c.fail(at, ErrInvalid, "%s is empty", what)
	case len(s) > MaxIDBytes:
		c.fail(at, ErrTooLarge, "%s is longer than %d bytes", what, MaxIDBytes)
	case !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }):
		c.fail(at, ErrInvalid, "%s %q has spaces or characters that do not print", what, s)
	default:
		return true
	}
	return false
}

// str checks a string within the limit.
func (c *checker) str(at string, v any) bool {
	s, ok := v.(string)
	switch {
	case !ok:
		c.fail(at, ErrInvalid, "must be a string")
	case len(s) > MaxStringBytes:
		c.fail(at, ErrTooLarge, "the string is %d bytes, over the limit of %d", len(s), MaxStringBytes)
	case !utf8.ValidString(s):
		c.fail(at, ErrInvalid, "the string is not UTF-8")
	default:
		return true
	}
	return false
}

// url checks a literal URL: absolute http or https, with a host.
func (c *checker) url(at, s string) {
	if !SafeURL(s) {
		c.fail(at, ErrUnsafeURL, "%q is not an absolute http or https URL", s)
	}
}

// SafeURL reports whether s is an absolute http or https URL with a host:
// the URLs a surface may show literally. A renderer applies the same rule to
// URLs bound from the data model.
func SafeURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// depth checks that a JSON value nests at most MaxDepth deep, and that its
// strings are within the limit.
func (c *checker) depth(at string, v any, d int) {
	if d > MaxDepth {
		c.fail(at, ErrTooLarge, "the value nests deeper than %d", MaxDepth)
		return
	}
	switch v := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if c.full() {
				return
			}
			c.depth(at+"/"+escape(k), v[k], d+1)
		}
	case []any:
		for i, item := range v {
			if c.full() {
				return
			}
			c.depth(at+"/"+strconv.Itoa(i), item, d+1)
		}
	case string:
		c.str(at, v)
	}
}

// tree checks a surface's components: a root, every reference defined, no
// cycle, and a depth within the limit.
func (c *checker) tree(surfaceID string, s *surface) {
	if len(s.defs) > MaxComponents {
		c.fail(s.first, ErrTooLarge, "surface %q has %d components, over the limit of %d", surfaceID, len(s.defs), MaxComponents)
		return
	}
	if _, ok := s.defs[RootID]; !ok {
		c.fail(s.first, ErrMissingRoot, "surface %q has no component with id %q: the tree starts there, and each call that updates a surface's components sends the whole tree", surfaceID, RootID)
	}
	ids := slices.Sorted(maps.Keys(s.defs))
	for _, id := range ids {
		for _, r := range s.defs[id].refs {
			if _, ok := s.defs[r.id]; !ok {
				c.fail(r.path, ErrDanglingReference, "no component has id %q: define it in this call (each call sends the whole tree)", r.id)
			}
		}
	}
	// Depth-first from each component, root first: grey components are on
	// the path, a reference to one is a cycle.
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	depth := map[string]int{} // longest path below, for black components
	var path []string
	var visit func(id string, at string) int
	visit = func(id, at string) int {
		switch color[id] {
		case grey:
			i := slices.Index(path, id)
			c.fail(at, ErrCycle, "the components contain themselves: %s", strings.Join(append(slices.Clone(path[i:]), id), " → "))
			return 0
		case black:
			return depth[id]
		}
		d, ok := s.defs[id]
		if !ok {
			return 0
		}
		color[id] = grey
		path = append(path, id)
		deepest := 0
		for _, r := range d.refs {
			if c.full() {
				break
			}
			deepest = max(deepest, 1+visit(r.id, r.path))
		}
		path = path[:len(path)-1]
		color[id] = black
		depth[id] = deepest
		return deepest
	}
	if d := visit(RootID, s.first); d >= MaxDepth {
		c.fail(s.first, ErrTooLarge, "surface %q's tree is %d deep, over the limit of %d", surfaceID, d+1, MaxDepth)
	}
	for _, id := range ids {
		if color[id] == white && !c.full() {
			visit(id, s.defs[id].path)
		}
	}
}

// isDate reports whether s is a date, a time or a date-time.
func isDate(s string) bool {
	for _, layout := range []string{time.DateOnly, "15:04", time.TimeOnly, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// asMap is v as an object, or nil.
func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// escape is a key as a JSON pointer token.
func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

// jsonText is v as JSON, for messages.
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil || len(b) > 64 {
		return "(that value)"
	}
	return string(b)
}
