package toolsettest

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// secretShapes are strings no cassette may hold: known key formats and
// cookies. A recording that would write one is refused, and a repository
// test scans every testdata/cassettes for them.
var secretShapes = []struct {
	what string
	re   *regexp.Regexp
}{
	{"an Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`)},
	{"a Tavily API key", regexp.MustCompile(`tvly-[A-Za-z0-9_-]{8,}`)},
	{"an sk- API key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}`)},
	{"a GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`)},
	{"an AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"a bearer token", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
	{"a Set-Cookie header", regexp.MustCompile(`(?im)^set-cookie:`)},
}

// credential matches a field or parameter named like a credential and its
// value: "api_key": "...", token=..., X-Secret: ...
var credential = regexp.MustCompile(`(?i)["']?([a-z0-9_.-]*(?:key|token|secret|password))["']?\s*[:=]\s*["']?([A-Za-z0-9_\-./+=]{20,})`)

// findSecrets returns what in data looks like a secret, described without
// the secret itself.
func findSecrets(data []byte) []string {
	var found []string
	for _, s := range secretShapes {
		if loc := s.re.FindIndex(data); loc != nil {
			found = append(found, fmt.Sprintf("%s at byte %d", s.what, loc[0]))
		}
	}
	for _, m := range credential.FindAllSubmatchIndex(data, -1) {
		name, value := strings.ToLower(string(data[m[2]:m[3]])), string(data[m[4]:m[5]])
		// Pagination cursors are long and random too, and not secrets.
		if strings.Contains(name, "page") || strings.Contains(name, "cursor") || strings.Contains(name, "next") {
			continue
		}
		if entropy(value) >= 3.5 {
			found = append(found, fmt.Sprintf("a high-entropy value of %q at byte %d", data[m[2]:m[3]], m[4]))
		}
	}
	return found
}

// entropy is the Shannon entropy of s in bits per character: random keys
// score over 4, words and repeated characters well under 3.5.
func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}
