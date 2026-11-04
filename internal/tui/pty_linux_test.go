//go:build linux

package tui

// This file exercises the real program through a pseudo-terminal. It builds the
// command once, allocates a PTY for each terminal size, drives the interface with
// bytes exactly as a terminal would deliver them, and checks the frames that come
// back. It is the only test that can observe the alternate-screen and cursor
// restore sequences, which a pure model test cannot. It is skipped with -short and
// wherever a PTY is unavailable.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"uo-unburger/internal/project"
)

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func ptyIoctl(fd, req, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

// openPTY allocates a pseudo-terminal and returns its master and slave files.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var n uint32
	if err = ptyIoctl(master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		master.Close()
		return nil, nil, err
	}
	var unlock int32
	if err = ptyIoctl(master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func setPTYSize(f *os.File, cols, rows int) error {
	ws := winsize{Row: uint16(rows), Col: uint16(cols)}
	return ptyIoctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

// ptyCapture accumulates everything read from the PTY so a test can search the
// frames it has seen so far.
type ptyCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *ptyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *ptyCapture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Len()
}

// from returns the output received after mark.
func (c *ptyCapture) from(mark int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buf.Bytes()
	if mark < 0 || mark > len(b) {
		return ""
	}
	return string(b[mark:])
}

// waitForFrom waits until the output received after mark contains needle.
func (c *ptyCapture) waitForFrom(mark int, needle string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if strings.Contains(c.from(mark), needle) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// buildCommand builds the real binary once for the PTY test.
func buildCommand(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the Go toolchain is not on PATH")
	}
	root, err := project.FindRoot()
	if err != nil {
		t.Skipf("project root not found: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "uo-unburger")
	cmd := exec.Command(goBin, "build", "-o", bin, "./cmd/uo-unburger")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return bin
}

// TestPTYFramesNavigationFocusAndTerminalRestore drives the interface in a real PTY
// at three terminal sizes.
func TestPTYFramesNavigationFocusAndTerminalRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("PTY integration test skipped in -short mode")
	}
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx on this system")
	}
	bin := buildCommand(t)
	for _, size := range []struct{ w, h int }{{80, 24}, {100, 30}, {40, 10}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			runPTYScenario(t, bin, size.w, size.h)
		})
	}
}

func runPTYScenario(t *testing.T, bin string, cols, rows int) {
	t.Helper()
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	defer master.Close()
	if err := setPTYSize(master, cols, rows); err != nil {
		slave.Close()
		t.Fatalf("setting the PTY size: %v", err)
	}

	cmd := exec.Command(bin)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		t.Fatalf("starting the interface: %v", err)
	}
	slave.Close() // the child owns the slave now

	var out ptyCapture
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, master)
		close(done)
	}()

	write := func(s string) {
		if _, err := master.WriteString(s); err != nil {
			t.Fatalf("writing %q to the PTY: %v", s, err)
		}
	}

	compact := cols < minUsableWidth || rows < minUsableHeight
	if compact {
		if !out.waitForFrom(0, "Terminal too small", 8*time.Second) {
			t.Fatalf("no compact frame at %dx%d:\n%q", cols, rows, out.from(0))
		}
		if !out.waitForFrom(0, "need 48x12", 3*time.Second) {
			t.Fatalf("the compact frame lost the required size:\n%q", out.from(0))
		}
	} else {
		if !out.waitForFrom(0, "status, warnings and what to do next", 8*time.Second) {
			t.Fatalf("no Overview frame at %dx%d:\n%q", cols, rows, out.from(0))
		}

		// Navigation: the digit accelerator opens Game files...
		mark := out.len()
		write("2")
		if !out.waitForFrom(mark, "where the game files, translation files and output folders live", 8*time.Second) {
			t.Fatalf("digit navigation did not reach Game files:\n%q", out.from(mark))
		}
		// ...and Escape returns to the Overview (its subtitle is unique to it).
		mark = out.len()
		write("\x1b")
		if !out.waitForFrom(mark, "status, warnings and what to do next", 8*time.Second) {
			t.Fatalf("Escape did not return to the Overview:\n%q", out.from(mark))
		}

		// Selection and activation: arrow down to "Open Translations", then enter.
		mark = out.len()
		write("\x1b[B")
		write("\r")
		if !out.waitForFrom(mark, "check the translation files", 8*time.Second) {
			t.Fatalf("arrow selection + enter did not open Translations:\n%q", out.from(mark))
		}

		// Focus: tab moves from the table to the actions pane, whose footer hint
		// is the visible proof the focus ring moved ("enter: run" only appears
		// when the actions pane owns the focus).
		mark = out.len()
		write("\t")
		if !out.waitForFrom(mark, "enter: run", 8*time.Second) {
			t.Fatalf("tab did not move focus to the actions pane:\n%q", out.from(mark))
		}
	}

	// Quit and confirm the process ends and the terminal is restored.
	write("q")
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the interface did not exit after q at %dx%d:\n%q", cols, rows, out.from(0))
	}
	_ = cmd.Wait()
	full := out.from(0)
	for _, seq := range []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?25h"} {
		if !strings.Contains(full, seq) {
			t.Fatalf("%dx%d: missing terminal sequence %q (alt-screen enter/leave and cursor show):\n%q",
				cols, rows, seq, full)
		}
	}
}
