package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"net/url"

	"github.com/bsamanvith/agentkit/connectors/google/common"
)

// upload sends metadata and content as one multipart/related request, which
// Drive reads as a file's details followed by its bytes (uploadType=multipart).
func (d *drive) upload(ctx context.Context, method, path string, query url.Values, meta map[string]any, content, contentType string, out *apiFile) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(part).Encode(meta); err != nil {
		return err
	}
	if part, err = w.CreatePart(textproto.MIMEHeader{"Content-Type": {contentType}}); err != nil {
		return err
	}
	_, _ = part.Write([]byte(content))
	if err := w.Close(); err != nil {
		return err
	}
	q := url.Values{"uploadType": {"multipart"}}
	for k, v := range query {
		q[k] = v
	}
	return d.client.DoJSON(ctx, common.Request{
		Method: method, Path: path, Query: q, Raw: buf.Bytes(),
		Header: map[string][]string{"Content-Type": {"multipart/related; boundary=" + w.Boundary()}},
	}, out)
}
