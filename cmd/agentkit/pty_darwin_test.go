package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair: the master (the test's end) and the
// slave (the process's terminal). It uses only syscall, so the tests need no
// pty library.
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var name [128]byte
	for _, req := range []struct {
		op  uintptr
		arg uintptr
	}{
		{syscall.TIOCPTYGRANT, 0},
		{syscall.TIOCPTYUNLK, 0},
		{syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))},
	} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), req.op, req.arg); errno != 0 {
			_ = m.Close()
			return nil, nil, errno
		}
	}
	n := bytes.IndexByte(name[:], 0)
	if n < 0 {
		n = len(name)
	}
	s, err := os.OpenFile(string(name[:n]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	return m, s, nil
}
