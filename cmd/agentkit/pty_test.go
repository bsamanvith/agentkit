package main

// A pseudo-terminal for the chat e2e test, so that chat runs as it does for a
// person: on a terminal, in cooked mode, where Ctrl-C becomes SIGINT and
// Ctrl-D the end of input.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ptyProcess is a process running on a pseudo-terminal; the test types into
// the master end and reads everything the process writes.
type ptyProcess struct {
	cmd    *exec.Cmd
	master *os.File
	done   chan struct{} // closed when the process has exited

	mu      sync.Mutex
	out     bytes.Buffer
	changed chan struct{} // closed and replaced on every read
	read    int           // how far expect has consumed out
}

// startPTY starts cmd with a new pseudo-terminal as its controlling terminal,
// stdin, stdout and stderr.
func startPTY(t *testing.T, cmd *exec.Cmd) *ptyProcess {
	t.Helper()
	master, slave, err := openPTY()
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("no pseudo-terminals on this OS")
	}
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close() // the process has its own copy
	p := &ptyProcess{cmd: cmd, master: master, done: make(chan struct{}), changed: make(chan struct{})}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			p.mu.Lock()
			p.out.Write(buf[:n])
			close(p.changed)
			p.changed = make(chan struct{})
			p.mu.Unlock()
			if err != nil { // EIO on Linux once the process has closed the terminal
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
		_ = master.Close()
		<-readDone
		t.Logf("terminal transcript:\n%s", p.transcript())
	})
	return p
}

// typeIn writes to the terminal as if typed.
func (p *ptyProcess) typeIn(t *testing.T, s string) {
	t.Helper()
	if _, err := p.master.WriteString(s); err != nil {
		t.Fatalf("type %q: %v", s, err)
	}
}

// expect waits until the output after what earlier calls consumed contains
// want, and consumes it; it returns the output up to and including want.
func (p *ptyProcess) expect(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(time.Minute) // only a broken chat gets here
	for {
		p.mu.Lock()
		rest := p.out.String()[p.read:]
		changed := p.changed
		if i := strings.Index(rest, want); i >= 0 {
			p.read += i + len(want)
			p.mu.Unlock()
			return rest[:i+len(want)]
		}
		p.mu.Unlock()
		select {
		case <-changed:
		case <-p.done:
			// The process may have written want just before exiting.
			p.mu.Lock()
			found := strings.Contains(p.out.String()[p.read:], want)
			p.mu.Unlock()
			if !found {
				t.Fatalf("the process exited (code %d) before writing %q", p.cmd.ProcessState.ExitCode(), want)
			}
		case <-deadline:
			t.Fatalf("no %q within a minute", want)
		}
	}
}

func (p *ptyProcess) transcript() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}
