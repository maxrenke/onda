//go:build windows

package player

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
)

// ipcAddress is suffixed with our PID so a stale mpv from a previous session can
// never own the pipe name a new session dials — with a fixed name, Windows could
// connect the new session's client to the old orphan's server, and s/q would
// then control the wrong mpv instance.
func ipcAddress() string { return `\\.\pipe\onda-mpv-` + strconv.Itoa(os.Getpid()) }

func cleanupIPC(string) {} // named pipes vanish when mpv exits; nothing to remove

// configureCmd is a no-op on Windows; the process tree is torn down by
// killTree via taskkill /T rather than by a process group.
func configureCmd(*exec.Cmd) {}

// killTree terminates pid *and its descendants*. This is what actually fixes
// the orphan: on Windows `mpv` on PATH is usually mpv.com, a console wrapper
// that runs the real mpv.exe as a child. (*os.Process).Kill only terminates the
// wrapper we spawned — it returns nil, so nothing looks wrong — while mpv.exe
// keeps holding the audio device and playing after onda exits.
func killTree(pid int) error {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// 128 = "process not found": it already exited, nothing left to kill.
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 128 {
		return nil
	}
	return fmt.Errorf("taskkill: %w: %s", err, bytes.TrimSpace(out))
}

func dialWithRetry(addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDialTimeout)
	defer cancel()
	for {
		conn, err := winio.DialPipeContext(ctx, addr)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(50 * time.Millisecond):
		}
	}
}
