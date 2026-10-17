package trunc_test

import (
	"testing"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/internal/trunc"
)

func TestCut(t *testing.T) {
	for _, tc := range []struct {
		s       string
		limit   int
		want    string
		wantCut int
	}{
		{"", 3, "", 0},
		{"abc", 3, "abc", 0},
		{"abcd", 3, "abc", 1},
		{"héllo", 2, "hé", 3},
		{"日本語", 0, "", 3},
		{"a\xffb", 2, "a�", 1},
	} {
		got, cut := trunc.Cut(tc.s, tc.limit)
		if got != tc.want || cut != tc.wantCut {
			t.Errorf("Cut(%q, %d) = %q, %d; want %q, %d", tc.s, tc.limit, got, cut, tc.want, tc.wantCut)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Cut(%q, %d) is not valid UTF-8", tc.s, tc.limit)
		}
	}
}

func TestShortAndContent(t *testing.T) {
	if got := trunc.Short("abcdef", 3, trunc.Marker); got != "abc…[truncated]" {
		t.Errorf("Short = %q", got)
	}
	if got := trunc.Short("abc", 3, trunc.Marker); got != "abc" {
		t.Errorf("Short of a short string = %q", got)
	}
	if got := trunc.Content("abcd", 3, "hint"); got != "abc…[truncated 1 character; hint]" {
		t.Errorf("Content one over = %q", got)
	}
	long := make([]byte, 12403)
	for i := range long {
		long[i] = 'x'
	}
	if got := trunc.Content(string(long), 3, "hint"); got != "xxx…[truncated 12,400 characters; hint]" {
		t.Errorf("Content = %q", got)
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 12400: "12,400", 1234567: "1,234,567", -1234: "-1,234", -123: "-123"} {
		if got := trunc.Thousands(n); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", n, got, want)
		}
	}
}
