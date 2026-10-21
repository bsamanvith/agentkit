//go:build unix

package file

import (
	"os"
	"syscall"
)

var errWouldBlock = syscall.EWOULDBLOCK

// flock takes an exclusive lock on f without waiting. The lock belongs to
// f's open file description, so a second open of the same file, even in
// this process, cannot take it.
func flock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EINTR {
			return err
		}
	}
}
