package drive

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	folderMime   = "application/vnd.google-apps.folder"
	googleAppsPf = "application/vnd.google-apps."
)

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	mimePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
)

// fileFields is what every metadata call asks Drive for.
const fileFields = "id,name,mimeType,description,modifiedTime,createdTime,size,parents,webViewLink,trashed,shared,owners(displayName,emailAddress)"

// file is a Drive file as the tools show it.
type file struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	MimeType     string   `json:"mime_type"`
	IsFolder     bool     `json:"is_folder,omitempty"`
	Description  string   `json:"description,omitempty"`
	ModifiedTime string   `json:"modified_time,omitempty"`
	CreatedTime  string   `json:"created_time,omitempty"`
	SizeBytes    int64    `json:"size_bytes,omitempty"`
	ParentIDs    []string `json:"parent_ids,omitempty"`
	WebLink      string   `json:"web_link,omitempty"`
	Owners       []string `json:"owners,omitempty"`
	Trashed      bool     `json:"trashed,omitempty"`
	Shared       bool     `json:"shared,omitempty"`
}

// apiFile is the wire shape of a Drive file.
type apiFile struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	MimeType     string   `json:"mimeType"`
	Description  string   `json:"description"`
	ModifiedTime string   `json:"modifiedTime"`
	CreatedTime  string   `json:"createdTime"`
	Size         string   `json:"size"`
	Parents      []string `json:"parents"`
	WebViewLink  string   `json:"webViewLink"`
	Trashed      bool     `json:"trashed"`
	Shared       bool     `json:"shared"`
	Owners       []struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	} `json:"owners"`
}

func (a apiFile) show() file {
	f := file{
		ID: a.ID, Name: a.Name, MimeType: a.MimeType, IsFolder: a.MimeType == folderMime,
		Description: a.Description, ModifiedTime: a.ModifiedTime, CreatedTime: a.CreatedTime,
		ParentIDs: a.Parents, WebLink: a.WebViewLink, Trashed: a.Trashed, Shared: a.Shared,
	}
	f.SizeBytes, _ = strconv.ParseInt(a.Size, 10, 64) // absent for Google-native files
	for _, o := range a.Owners {
		switch {
		case o.DisplayName != "" && o.EmailAddress != "":
			f.Owners = append(f.Owners, o.DisplayName+" <"+o.EmailAddress+">")
		case o.EmailAddress != "":
			f.Owners = append(f.Owners, o.EmailAddress)
		case o.DisplayName != "":
			f.Owners = append(f.Owners, o.DisplayName)
		}
	}
	return f
}

func checkID(what, id string) error {
	if !idPattern.MatchString(id) {
		return invalid("%s %q is not a Drive id (letters, digits, - and _); use an id from drive_search", what, clip(id, 40))
	}
	return nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// quote escapes s for a single-quoted string in Drive's query language.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, `'`, `\'`) + "'"
}

// parseTime accepts an RFC 3339 time or a YYYY-MM-DD date.
func parseTime(what, s string) (string, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", invalid("%s %q: want a date like 2026-10-01 or a time like 2026-10-01T09:00:00Z", what, clip(s, 40))
}

// truncate cuts s to n runes at a rune boundary.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
