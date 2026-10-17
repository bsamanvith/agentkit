package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The fields of a tool definition that [Fingerprint] covers and [Diff] names.
const (
	FieldName         = "name"
	FieldTitle        = "title"
	FieldDescription  = "description"
	FieldInputSchema  = "inputSchema"
	FieldOutputSchema = "outputSchema"
	FieldAnnotations  = "annotations"
)

// Fingerprint is a stable hash of what a model and a reviewer see of a tool:
// its name, title, raw description, input and output schemas and
// annotations, as canonical JSON (keys sorted, no insignificant space,
// numbers as written). It is "sha256:" and 64 hex digits. Pin it when a tool
// is approved: a server that changes the tool changes its fingerprint.
func Fingerprint(t *mcp.Tool) (string, error) {
	fields, err := canonicalFields(t)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range fieldOrder {
		// Length-prefixed, so no field can run into the next.
		fmt.Fprintf(h, "%s:%d:", f, len(fields[f]))
		h.Write(fields[f])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Diff names the fields that differ between two versions of a tool, in the
// order of the Field constants; none means the fingerprints are equal.
func Diff(prev, next *mcp.Tool) ([]string, error) {
	a, err := canonicalFields(prev)
	if err != nil {
		return nil, err
	}
	b, err := canonicalFields(next)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, f := range fieldOrder {
		if !bytes.Equal(a[f], b[f]) {
			changed = append(changed, f)
		}
	}
	return changed, nil
}

var fieldOrder = []string{FieldName, FieldTitle, FieldDescription, FieldInputSchema, FieldOutputSchema, FieldAnnotations}

func canonicalFields(t *mcp.Tool) (map[string][]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("mcp client: fingerprint of a nil tool")
	}
	out := make(map[string][]byte, len(fieldOrder))
	for f, v := range map[string]any{
		FieldName: t.Name, FieldTitle: t.Title, FieldDescription: t.Description,
		FieldInputSchema: t.InputSchema, FieldOutputSchema: t.OutputSchema, FieldAnnotations: t.Annotations,
	} {
		b, err := canonical(v)
		if err != nil {
			return nil, fmt.Errorf("mcp client: tool %q: %s: %w", t.Name, f, err)
		}
		out[f] = b
	}
	return out, nil
}

// canonical encodes v as canonical JSON: decoded and encoded again, so map
// keys are sorted and space is gone, with numbers kept as written. A nil
// value (or a nil pointer) is null.
func canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
