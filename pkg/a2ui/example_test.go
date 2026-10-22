package a2ui_test

import (
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
)

func ExampleParseInput() {
	// A model's ui_render call whose button names a label it never defined.
	input := []byte(`{"messages": [
	  {"version": "v0.9", "createSurface": {"surfaceId": "confirm", "catalogId": "` + a2ui.BasicCatalogID + `"}},
	  {"version": "v0.9", "updateComponents": {"surfaceId": "confirm", "components": [
	    {"id": "root", "component": "Button", "child": "label", "action": {"event": {"name": "confirm"}}}
	  ]}}
	]}`)
	_, err := a2ui.ParseInput(input)
	fmt.Println(errors.Is(err, a2ui.ErrDanglingReference))
	fmt.Println(err)

	// The corrected call.
	input = []byte(`{"messages": [
	  {"version": "v0.9", "createSurface": {"surfaceId": "confirm", "catalogId": "` + a2ui.BasicCatalogID + `"}},
	  {"version": "v0.9", "updateComponents": {"surfaceId": "confirm", "components": [
	    {"id": "root", "component": "Button", "child": "label", "action": {"event": {"name": "confirm"}}},
	    {"id": "label", "component": "Text", "text": "Confirm"}
	  ]}}
	]}`)
	msgs, err := a2ui.ParseInput(input)
	fmt.Println(len(msgs), msgs[1].Kind(), msgs[1].SurfaceID(), err)
	// Output:
	// true
	// /messages/1/updateComponents/components/0/child: no component has id "label": define it in this call (each call sends the whole tree)
	// 2 updateComponents confirm <nil>
}
