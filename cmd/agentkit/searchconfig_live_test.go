//go:build live

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// searchConfig is deploy/local/agent/search.json with its paths (the
// skills folder, the handbook) made absolute, so that it works from the
// temporary directory startServe writes it to.
func searchConfig(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "deploy", "local", "agent"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "search.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer(
		`{"path": "skills"}`, `{"path": "`+filepath.Join(dir, "skills")+`"}`,
		`"root": "knowledge"`, `"root": "`+filepath.Join(dir, "knowledge")+`"`,
	).Replace(string(b))
	if !strings.Contains(s, filepath.Join(dir, "skills")) || !strings.Contains(s, filepath.Join(dir, "knowledge")) {
		t.Fatal("search.json's skills or knowledge path changed: update searchConfig")
	}
	return s
}
