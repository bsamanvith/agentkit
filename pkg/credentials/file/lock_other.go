//go:build !unix

package file

import (
	"errors"
	"os"
)

var errWouldBlock = errors.New("would block")

// flock is not built on this system: the file backend needs flock(2).
func flock(*os.File) error {
	return errors.New("the credentials file backend needs a Unix system (flock)")
}
