package fc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Process is a running Firecracker VMM.
type Process struct {
	Cmd     *exec.Cmd
	Socket  string
	WorkDir string
	Client  *Client
	logFile *os.File
}

// ProcessConfig controls how Firecracker is launched.
type ProcessConfig struct {
	Binary  string // path to the firecracker binary
	WorkDir string // process cwd; relative resource paths in snapshots resolve against it
	LogPath string // where stdout/stderr (incl. guest serial console) go; empty = discard
}

// Start launches Firecracker with its API socket inside WorkDir.
// It returns as soon as the process has been spawned; call WaitForSocket next.
func Start(cfg ProcessConfig) (*Process, error) {
	wd, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(wd, "fc.sock")
	_ = os.Remove(sock)

	cmd := exec.Command(cfg.Binary, "--api-sock", sock)
	cmd.Dir = wd
	// Own process group so we can kill it cleanly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	p := &Process{Cmd: cmd, Socket: sock, WorkDir: wd, Client: NewClient(sock)}
	if cfg.LogPath != "" {
		f, err := os.Create(cfg.LogPath)
		if err != nil {
			return nil, err
		}
		p.logFile = f
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		p.closeLog()
		return nil, fmt.Errorf("start firecracker: %w", err)
	}
	return p, nil
}

// WaitForSocket polls until the API socket accepts connections.
func (p *Process) WaitForSocket(ctx context.Context, poll time.Duration) error {
	for {
		c, err := net.Dial("unix", p.Socket)
		if err == nil {
			c.Close()
			return nil
		}
		if p.Cmd.ProcessState != nil {
			return errors.New("firecracker exited before API socket was ready")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for API socket: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

// Kill terminates the VMM and waits for it to exit.
func (p *Process) Kill() {
	if p.Cmd.Process != nil {
		_ = syscall.Kill(-p.Cmd.Process.Pid, syscall.SIGKILL)
		_, _ = p.Cmd.Process.Wait()
	}
	p.closeLog()
	_ = os.Remove(p.Socket)
}

func (p *Process) closeLog() {
	if p.logFile != nil {
		p.logFile.Close()
		p.logFile = nil
	}
}

// PageFaults returns minor and major page faults of the VMM process so far,
// read from /proc/<pid>/stat. Guest memory faults on a File-backed restore
// show up here as faults of the Firecracker process.
func (p *Process) PageFaults() (minflt, majflt uint64, err error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.Cmd.Process.Pid))
	if err != nil {
		return 0, 0, err
	}
	// comm (field 2) may contain spaces; skip past the closing paren.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, errors.New("unexpected /proc/pid/stat format")
	}
	f := strings.Fields(s[i+1:])
	// After ')': f[0]=state(3) ... minflt is field 10 -> f[7], majflt field 12 -> f[9]
	if len(f) < 10 {
		return 0, 0, errors.New("short /proc/pid/stat")
	}
	minflt, _ = strconv.ParseUint(f[7], 10, 64)
	majflt, _ = strconv.ParseUint(f[9], 10, 64)
	return minflt, majflt, nil
}
