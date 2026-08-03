package player

import (
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEncodeCommand(t *testing.T) {
	got, err := encodeCommand(7, "loadfile", "http://x/stream")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"command":["loadfile","http://x/stream"],"request_id":7}` + "\n"
	if string(got) != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestParseLineEvent(t *testing.T) {
	f, err := parseLine([]byte(`{"event":"property-change","name":"media-title","data":"Now Playing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Event != "property-change" || f.Name != "media-title" || f.Data != "Now Playing" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}

func TestParseLineIgnoresReplies(t *testing.T) {
	f, err := parseLine([]byte(`{"error":"success","request_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Event != "" {
		t.Fatalf("reply should have empty Event, got %q", f.Event)
	}
}

func TestParseLineEndFileReason(t *testing.T) {
	f, err := parseLine([]byte(`{"event":"end-file","reason":"error"}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Event != "end-file" || f.Reason != "error" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}

func TestNewRequiresMpv(t *testing.T) {
	// With a bogus binary name, New must fail fast and not leak a process.
	_, err := New(Options{Binary: "definitely-not-mpv-xyz"})
	if err == nil {
		t.Fatal("expected error when mpv binary is missing")
	}
}

// TestNewDoesNotStartMpv guards the fix for onda grabbing the audio device at
// launch: New must only verify mpv is on PATH, not spawn it or touch IPC.
func TestNewDoesNotStartMpv(t *testing.T) {
	if _, err := lookPathMpv(); err != nil {
		t.Skip("mpv not available:", err)
	}
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.started {
		t.Fatal("New must not start mpv")
	}
	if p.cmd != nil || p.conn != nil {
		t.Fatal("New must not spawn a process or open an IPC connection")
	}
	if _, err := os.Stat(p.sock); err == nil {
		t.Fatal("New must not create the IPC socket file")
	}

	// Events() must be usable before mpv starts (no panic on nil channel, no
	// spurious events since nothing has happened yet).
	select {
	case e := <-p.Events():
		t.Fatalf("unexpected event before Play: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestPlayStartsMpv proves the other half of the lazy-start contract: calling
// Play() is what actually spawns mpv and connects IPC.
func TestPlayStartsMpv(t *testing.T) {
	if _, err := lookPathMpv(); err != nil {
		t.Skip("mpv not available:", err)
	}
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Play("http://example.invalid/stream"); err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	started := p.started
	hasConn := p.conn != nil
	p.mu.Unlock()
	if !started || !hasConn {
		t.Fatal("Play must start mpv and connect IPC")
	}
	if _, err := os.Stat(p.sock); err != nil {
		t.Fatalf("expected IPC socket to exist after Play: %v", err)
	}
}

// TestNoopsBeforeStart ensures control methods degrade to no-ops instead of
// erroring, blocking, or starting mpv when called before Play has ever run.
func TestNoopsBeforeStart(t *testing.T) {
	if _, err := lookPathMpv(); err != nil {
		t.Skip("mpv not available:", err)
	}
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop before start: %v", err)
	}
	if err := p.Pause(); err != nil {
		t.Fatalf("Pause before start: %v", err)
	}
	if err := p.Resume(); err != nil {
		t.Fatalf("Resume before start: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close before start: %v", err)
	}
	if p.started {
		t.Fatal("no-op control methods must not start mpv")
	}
}

// TestVolumeBeforeStartIsStored verifies Volume() stores the desired volume
// instead of erroring or starting mpv when called before Play.
func TestVolumeBeforeStartIsStored(t *testing.T) {
	if _, err := lookPathMpv(); err != nil {
		t.Skip("mpv not available:", err)
	}
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Volume(42); err != nil {
		t.Fatal(err)
	}
	if p.started {
		t.Fatal("Volume must not start mpv")
	}
	if p.volume == nil || *p.volume != 42 {
		t.Fatalf("expected stored volume 42, got %v", p.volume)
	}
}

func lookPathMpv() (string, error) {
	return exec.LookPath("mpv")
}

func TestIPCAddressIsProcessUnique(t *testing.T) {
	addr := ipcAddress()
	if !strings.Contains(addr, strconv.Itoa(os.Getpid())) {
		t.Fatalf("ipcAddress must be unique per process, got %q", addr)
	}
	if addr != ipcAddress() {
		t.Fatalf("ipcAddress must be stable within a process, got %q then %q", addr, ipcAddress())
	}
}

func TestReapNilIsNoError(t *testing.T) {
	if err := reap(nil); err != nil {
		t.Fatalf("reap(nil) = %v, want nil", err)
	}
	if err := reap(&exec.Cmd{}); err != nil {
		t.Fatalf("reap(unstarted) = %v, want nil", err)
	}
}

func pidAlive(t *testing.T, pid int) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
		if err != nil {
			t.Fatalf("tasklist: %v", err)
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// TestReapKillsStartedProcess guards the orphan bug: `mpv` on PATH is often a
// wrapper (mpv.com on Windows) whose child keeps playing when only the direct
// process is killed, so assert nothing named mpv survives the reap.
func TestReapKillsStartedProcess(t *testing.T) {
	bin, err := exec.LookPath("mpv")
	if err != nil {
		t.Skip("mpv not installed")
	}
	before := mpvPids(t)
	cmd := exec.Command(bin, "--idle=yes", "--no-video", "--no-terminal")
	configureCmd(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	time.Sleep(750 * time.Millisecond) // let a wrapper spawn its child
	if err := reap(cmd); err != nil {
		t.Fatalf("reap returned %v", err)
	}
	time.Sleep(750 * time.Millisecond)
	if pidAlive(t, pid) {
		t.Fatalf("pid %d still alive after reap", pid)
	}
	for _, p := range mpvPids(t) {
		if !slices.Contains(before, p) {
			t.Fatalf("reap left an orphaned mpv process (pid %s) behind", p)
		}
	}
}

// mpvPids lists every running mpv process, so the test can tell a leftover
// child apart from unrelated mpv instances that were already running.
func mpvPids(t *testing.T) []string {
	t.Helper()
	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		out, err = exec.Command("tasklist", "/FI", "IMAGENAME eq mpv.exe", "/NH", "/FO", "CSV").Output()
	} else {
		out, _ = exec.Command("pgrep", "-x", "mpv").Output()
		var pids []string
		for _, l := range strings.Fields(string(out)) {
			pids = append(pids, l)
		}
		return pids
	}
	if err != nil {
		t.Fatalf("tasklist: %v", err)
	}
	var pids []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\",\"")
		if len(f) > 1 {
			pids = append(pids, f[1])
		}
	}
	return pids
}
