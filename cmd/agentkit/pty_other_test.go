//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

func openPTY() (master, slave *os.File, err error) {
	return nil, nil, errors.ErrUnsupported
}
