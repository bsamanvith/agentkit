package drive

import (
	"regexp"
	"strings"

	"github.com/bsamanvith/agentkit/connectors/google/common"
)

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	mimePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
)

const (
	folderMime = "application/vnd.google-apps.folder"
	nativeMime = "application/vnd.google-apps."
)

// quote returns s as a single-quoted string of Drive's query language: the
// backslash and the quote are escaped, so s cannot end the string and add
// terms of its own.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, `'`, `\'`) + "'"
}

// checkID refuses what is not a Drive id, so ids put into a query or a path
// are only ever ids.
func checkID(what, id string) error {
	if !idPattern.MatchString(id) {
		return common.Invalid("%s %q is not a Drive id (letters, digits, - and _); use an id from drive_search_files", what, common.Truncate(id, 40))
	}
	return nil
}

// searchQuery builds the q of a search from the inputs, or returns driveQuery
// as it is.
func searchQuery(in searchIn) (string, error) {
	if in.DriveQuery != "" {
		if in.Query != "" || in.MimeType != "" || in.FolderID != "" {
			return "", common.Invalid("drive_query is a whole query by itself; do not combine it with query, mime_type or folder_id")
		}
		return in.DriveQuery, nil
	}
	terms := []string{"trashed = false"}
	if in.Query != "" {
		q := quote(in.Query)
		terms = append(terms, "(name contains "+q+" or fullText contains "+q+")")
	}
	if in.MimeType != "" {
		mt := in.MimeType
		if mt == "folder" {
			mt = folderMime
		}
		if !mimePattern.MatchString(mt) {
			return "", common.Invalid("mime_type %q is not a media type such as application/pdf (or folder)", common.Truncate(mt, 40))
		}
		terms = append(terms, "mimeType = "+quote(mt))
	}
	if in.FolderID != "" {
		if err := checkID("folder_id", in.FolderID); err != nil {
			return "", err
		}
		terms = append(terms, quote(in.FolderID)+" in parents")
	}
	return strings.Join(terms, " and "), nil
}
