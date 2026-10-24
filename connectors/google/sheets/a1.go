package sheets

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/bsamanvith/agentkit/connectors/google/common"
)

const maxRangeLen = 200

var (
	idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{10,100}$`)
	// cellsPattern is the part of an A1 range after "!": cells (A1, A1:D20,
	// A:D) or rows (1:5).
	cellsPattern = regexp.MustCompile(`^(?:[A-Za-z]{1,3}[0-9]{0,7}(?::[A-Za-z]{1,3}[0-9]{0,7})?|[0-9]{1,7}:[0-9]{1,7})$`)
)

func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return common.Invalid("spreadsheet_id %q is not a spreadsheet id: copy it from the spreadsheet's URL (docs.google.com/spreadsheets/d/<id>/)", common.Truncate(id, 60))
	}
	return nil
}

// checkRange accepts an A1 range: a tab name ("Sheet1", or quoted with ”
// for a quote: "'Q3 ”final”'"), optionally "!" and cells ("A1:D20"). A
// bare cells part ("A1:D20") reads the first tab. It refuses anything else
// before a request is sent.
func checkRange(r string) error {
	bad := func(why string) error {
		return common.Invalid("range %q: %s; use A1 notation such as Sheet1!A1:D20, 'My tab'!A:C or a tab name", common.Truncate(r, 60), why)
	}
	switch {
	case strings.TrimSpace(r) == "":
		return bad("it is empty")
	case len(r) > maxRangeLen:
		return bad("it is too long")
	case strings.IndexFunc(r, unicode.IsControl) >= 0:
		return bad("it has a control character")
	}
	tab, cells, err := splitRange(r)
	if err != nil {
		return bad(err.Error())
	}
	if tab == "" && cells == "" {
		return bad("it names no tab or cells")
	}
	if cells != "" && !cellsPattern.MatchString(cells) {
		return bad("the cells part is not A1 notation")
	}
	return nil
}

type rangeError string

func (e rangeError) Error() string { return string(e) }

// splitRange splits an A1 range into its tab (unquoted) and cells parts.
// Without "!", a range that is cells is cells, anything else a tab name.
func splitRange(r string) (tab, cells string, err error) {
	if strings.HasPrefix(r, "'") {
		var b strings.Builder
		for i := 1; i < len(r); i++ {
			if r[i] != '\'' {
				b.WriteByte(r[i])
				continue
			}
			if i+1 < len(r) && r[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			rest := r[i+1:]
			switch {
			case rest == "":
				return b.String(), "", nil
			case strings.HasPrefix(rest, "!"):
				return b.String(), rest[1:], nil
			}
			return "", "", rangeError("a quoted tab name must be followed by ! or nothing")
		}
		return "", "", rangeError("a quoted tab name is not closed")
	}
	if t, c, ok := strings.Cut(r, "!"); ok {
		if strings.ContainsAny(t, "'") {
			return "", "", rangeError("a tab name with a quote must be quoted, with the quote doubled")
		}
		return t, c, nil
	}
	if cellsPattern.MatchString(r) {
		return "", r, nil
	}
	if strings.Contains(r, "'") {
		return "", "", rangeError("a tab name with a quote must be quoted, with the quote doubled")
	}
	return r, "", nil
}

// quoteTab writes a tab name as A1 notation wants it.
func quoteTab(tab string) string {
	return "'" + strings.ReplaceAll(tab, "'", "''") + "'"
}

// nextRange is the range that continues a read of r (as Sheets echoes it,
// "Sheet1!A1:D20") after kept rows, or "" when r cannot say.
func nextRange(r string, kept int) string {
	tab, cells, err := splitRange(r)
	if err != nil || cells == "" {
		return ""
	}
	from, to, _ := strings.Cut(cells, ":")
	col, row := splitCell(from)
	if row == 0 {
		row = 1
	}
	next := col + strconv.Itoa(row+kept)
	if to != "" {
		next += ":" + to
	}
	if tab == "" {
		return next
	}
	return quoteTab(tab) + "!" + next
}

// splitCell splits "AB12" into "AB" and 12 (0 when there is no row).
func splitCell(c string) (string, int) {
	i := strings.IndexFunc(c, unicode.IsDigit)
	if i < 0 {
		return c, 0
	}
	n, _ := strconv.Atoi(c[i:])
	return c[:i], n
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
