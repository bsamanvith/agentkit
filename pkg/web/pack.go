//go:build ignore

// pack gzips the console build in place (web.Pack) after Vite has written
// it; make web and web/scripts/check-build.sh run it:
//
//	go run ./pkg/web/pack.go pkg/web/dist
package main

import (
	"fmt"
	"os"

	"github.com/bsamanvith/agentkit/pkg/web"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./pkg/web/pack.go <dist>")
		os.Exit(2)
	}
	if err := web.Pack(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
}
