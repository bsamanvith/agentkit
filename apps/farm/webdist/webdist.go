// Package webdist holds the farm app's web build (web/apps/farm), embedded
// so that the farm binary needs nothing beside it. The build is committed
// in dist/, gzipped as pkg/web serves it (make farm-web writes it), so go
// build needs no Node.js.
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the build.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // dist is embedded: it exists
	}
	return sub
}
