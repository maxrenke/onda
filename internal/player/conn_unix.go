//go:build !windows

package player

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// ipcAddress is suffixed with our PID so a stale mpv from a previous session can
// never own the socket path a new session dials.
func ipcAddress() string {
	return filepath.Join(os.TempDir(), "onda-mpv-"+strconv.Itoa(os.Getpid())+".sock")
}

func cleanupIPC(addr string) { _ = os.Remove(addr) }

// configureCmd puts mpv in its own process group so killTree can signal the
// whole group — a wrapper script that execs the real mpv would otherwise leave
// the player running when only the direct child is killed.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree terminates pid and its descendants (the process group created above).
func killTree(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		return err
	}
	return nil
}

func dialWithRetry(addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDialTimeout)
	defer cancel()
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", addr)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("timed out connecting to mpv IPC socket")
		case <-time.After(50 * time.Millisecond): // brief backoff, matches the Windows path
		}
	}
}
