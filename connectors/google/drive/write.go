package drive

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/bsamanvith/agentkit/connectors/google/common"
)

// create makes a file. Without content it is one JSON request; with content
// it is one multipart upload.
func (d *drive) create(ctx context.Context, in createIn) (file, error) {
	if strings.TrimSpace(in.Name) == "" {
		return file{}, common.Invalid("name is required")
	}
	if in.FolderID != "" {
		if err := checkID("folder_id", in.FolderID); err != nil {
			return file{}, err
		}
	}
	if len(in.Content) > maxUpload {
		return file{}, common.Invalid("content is over the %d MB limit", maxUpload>>20)
	}
	// What the file becomes, and the type its content is sent as.
	fileType, contentType := in.MimeType, in.MimeType
	switch in.MimeType {
	case "":
		fileType, contentType = "text/plain", "text/plain"
	case "text/plain", "text/markdown", "text/csv":
	case "google_doc":
		fileType, contentType = nativeMime+"document", "text/plain"
	case "google_sheet":
		fileType, contentType = nativeMime+"spreadsheet", "text/csv"
	default:
		return file{}, common.Invalid("mime_type %q: want one of text/plain, text/markdown, text/csv, google_doc, google_sheet", common.Truncate(in.MimeType, 40))
	}
	meta := map[string]any{"name": in.Name, "mimeType": fileType}
	if in.FolderID != "" {
		meta["parents"] = []string{in.FolderID}
	}
	query := url.Values{"fields": {fileFields}, "supportsAllDrives": {"true"}}
	var f apiFile
	var err error
	if in.Content == "" {
		err = d.client.DoJSON(ctx, common.Request{Method: http.MethodPost, Path: filesPath, Query: query, Body: meta}, &f)
	} else {
		err = d.upload(ctx, http.MethodPost, "/upload"+filesPath, query, meta, in.Content, contentType, &f)
	}
	if err != nil {
		return file{}, err
	}
	return f.show(), nil
}

// update renames a file and/or replaces its content.
func (d *drive) update(ctx context.Context, in updateIn) (file, error) {
	if err := checkID("file_id", in.FileID); err != nil {
		return file{}, err
	}
	if in.Name == nil && in.Content == nil {
		return file{}, common.Invalid("nothing to change: set name or content")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return file{}, common.Invalid("name cannot be empty")
	}
	if in.Content != nil && len(*in.Content) > maxUpload {
		return file{}, common.Invalid("content is over the %d MB limit", maxUpload>>20)
	}
	meta := map[string]any{}
	if in.Name != nil {
		meta["name"] = *in.Name
	}
	query := url.Values{"fields": {fileFields}, "supportsAllDrives": {"true"}}
	var f apiFile
	if in.Content == nil {
		err := d.client.DoJSON(ctx, common.Request{Method: http.MethodPatch, Path: common.Path(filesPath, in.FileID), Query: query, Body: meta}, &f)
		if err != nil {
			return file{}, err
		}
		return f.show(), nil
	}
	// The file's own type decides what content may replace it.
	cur, err := d.metadata(ctx, in.FileID)
	if err != nil {
		return file{}, err
	}
	var contentType string
	switch {
	case cur.MimeType == nativeMime+"document":
		contentType = "text/plain"
	case cur.MimeType == nativeMime+"spreadsheet":
		contentType = "text/csv"
	case strings.HasPrefix(cur.MimeType, nativeMime):
		return file{}, common.Invalid("%q is a Google %s, whose content cannot be replaced; make a new file instead",
			common.Truncate(cur.Name, 60), strings.TrimPrefix(cur.MimeType, nativeMime))
	case textual(cur.MimeType):
		contentType = cur.MimeType
	default:
		return file{}, common.Invalid("%q is %s, not a text file, so its content cannot be replaced", common.Truncate(cur.Name, 60), cur.MimeType)
	}
	if err := d.upload(ctx, http.MethodPatch, "/upload"+common.Path(filesPath, in.FileID), query, meta, *in.Content, contentType, &f); err != nil {
		return file{}, err
	}
	return f.show(), nil
}
