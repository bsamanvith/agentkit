package a2ui

import (
	"encoding/json"
	"errors"
	"strings"
)

// Protocol identifiers.
const (
	// Version is the "version" of every message: the v0.9 protocol family.
	Version = "v0.9"
	// SpecRelease is the release of the spec this package follows.
	SpecRelease = "0.9.1"
	// BasicCatalogID is the catalogId of the basic catalog, the only one
	// a surface may use here.
	BasicCatalogID = "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"
	// UIMode is the value of an agent's "ui" setting that gives it
	// ui_render.
	UIMode = "a2ui"
	// ToolName is the name of the tool ([Tool]).
	ToolName = "ui_render"
	// RootID is the ID of a surface's root component.
	RootID = "root"
)

// Limits. A call or message over one is [ErrTooLarge].
const (
	// MaxMessageBytes bounds one message, as JSON.
	MaxMessageBytes = 64 << 10
	// MaxInputBytes bounds one ui_render call's input.
	MaxInputBytes = 128 << 10
	// MaxMessages bounds the messages of one call.
	MaxMessages = 16
	// MaxComponents bounds the components of one surface in a call.
	MaxComponents = 200
	// MaxChildren bounds a child list, and MaxOptions a ChoicePicker's
	// options.
	MaxChildren = 100
	MaxOptions  = 100
	// MaxDepth bounds the component tree's depth from the root, and the
	// nesting of any JSON value.
	MaxDepth = 32
	// MaxIDBytes bounds a component or surface ID, and MaxStringBytes any
	// other string.
	MaxIDBytes     = 128
	MaxStringBytes = 8 << 10
	// MaxActionBytes bounds an action's context, as JSON.
	MaxActionBytes = 16 << 10
)

// Errors. Every problem a validation finds matches ErrInvalid and, where
// one fits, a more specific sentinel.
var (
	// ErrInvalid is any problem with a message or an action.
	ErrInvalid = errors.New("invalid A2UI message")
	// ErrUnknownComponent: the component type is not in the catalog.
	ErrUnknownComponent = wrap("unknown component")
	// ErrUnknownProperty: a property the component, message or object
	// does not have.
	ErrUnknownProperty = wrap("unknown property")
	// ErrDanglingReference: a child names a component that is not defined.
	ErrDanglingReference = wrap("dangling reference")
	// ErrMissingRoot: a surface's components have none with the ID "root".
	ErrMissingRoot = wrap("no root component")
	// ErrCycle: components contain themselves.
	ErrCycle = wrap("cycle")
	// ErrTooLarge: over a limit.
	ErrTooLarge = wrap("too large")
	// ErrUnsafeURL: a literal URL that is not an absolute http(s) URL.
	ErrUnsafeURL = wrap("unsafe URL")
	// ErrUnknownCatalog: a surface names a catalog other than the basic one.
	ErrUnknownCatalog = wrap("unknown catalog")
)

func wrap(what string) error { return &sentinel{what} }

// sentinel is an error that matches itself and ErrInvalid.
type sentinel struct{ what string }

func (s *sentinel) Error() string { return ErrInvalid.Error() + ": " + s.what }
func (s *sentinel) Unwrap() error { return ErrInvalid }

// ValidationError is one problem with a message: where (Path, a JSON pointer
// into the input that was checked: the message, or the call's input) and
// what (Err, a sentinel, and Detail, for the model that wrote it).
type ValidationError struct {
	Path   string
	Err    error
	Detail string
}

func (e *ValidationError) Error() string {
	p := e.Path
	if p == "" {
		p = "/"
	}
	return p + ": " + e.Detail
}

func (e *ValidationError) Unwrap() error { return e.Err }

// Errors is the problems a validation found, in the order found (at most
// maxProblems). It matches each problem's sentinel with errors.Is.
type Errors []*ValidationError

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

func (es Errors) Unwrap() []error {
	out := make([]error, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

// Message is one A2UI server-to-client message: exactly one of its four
// fields is set. Its JSON is the spec's.
type Message struct {
	Version          string            `json:"version"`
	CreateSurface    *CreateSurface    `json:"createSurface,omitempty"`
	UpdateComponents *UpdateComponents `json:"updateComponents,omitempty"`
	UpdateDataModel  *UpdateDataModel  `json:"updateDataModel,omitempty"`
	DeleteSurface    *DeleteSurface    `json:"deleteSurface,omitempty"`
}

// CreateSurface starts a surface, drawn with the catalog CatalogID.
type CreateSurface struct {
	SurfaceID string `json:"surfaceId"`
	CatalogID string `json:"catalogId"`
	// Theme holds the catalog's theme parameters: primaryColor ("#RRGGBB"),
	// iconUrl and agentDisplayName.
	Theme map[string]any `json:"theme,omitempty"`
	// SendDataModel asks the client to send the surface's data model with
	// every message to the agent.
	SendDataModel bool `json:"sendDataModel,omitempty"`
}

// UpdateComponents adds components to a surface, or replaces those with
// the same IDs.
type UpdateComponents struct {
	SurfaceID  string      `json:"surfaceId"`
	Components []Component `json:"components"`
}

// Component is one component: "id", "component" (its type) and the type's
// properties. Values are what encoding/json decodes into any (numbers as
// json.Number when read by this package) or any value that marshals to JSON.
type Component map[string]any

// ID is the component's "id", or "".
func (c Component) ID() string { s, _ := c["id"].(string); return s }

// Type is the component's "component", or "".
func (c Component) Type() string { s, _ := c["component"].(string); return s }

// UpdateDataModel replaces the value at Path (a JSON pointer; empty or "/"
// for the whole model) of a surface's data model, or removes it when Value
// is absent.
type UpdateDataModel struct {
	SurfaceID string          `json:"surfaceId"`
	Path      string          `json:"path,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
}

// DeleteSurface removes a surface.
type DeleteSurface struct {
	SurfaceID string `json:"surfaceId"`
}

// Kind is the message's type: "createSurface", "updateComponents",
// "updateDataModel" or "deleteSurface"; "" when none is set.
func (m Message) Kind() string {
	switch {
	case m.CreateSurface != nil:
		return kindCreate
	case m.UpdateComponents != nil:
		return kindUpdate
	case m.UpdateDataModel != nil:
		return kindData
	case m.DeleteSurface != nil:
		return kindDelete
	}
	return ""
}

// SurfaceID is the surface the message is about.
func (m Message) SurfaceID() string {
	switch {
	case m.CreateSurface != nil:
		return m.CreateSurface.SurfaceID
	case m.UpdateComponents != nil:
		return m.UpdateComponents.SurfaceID
	case m.UpdateDataModel != nil:
		return m.UpdateDataModel.SurfaceID
	case m.DeleteSurface != nil:
		return m.DeleteSurface.SurfaceID
	}
	return ""
}

const (
	kindCreate = "createSurface"
	kindUpdate = "updateComponents"
	kindData   = "updateDataModel"
	kindDelete = "deleteSurface"
)
