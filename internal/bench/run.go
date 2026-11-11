package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"fcbench/internal/fc"
)

type Config struct {
	Firecracker  string
	SnapshotDir  string
	WorkDir      string // per-run directories are created under here
	Runs         int
	VsockPort    uint32 // 0 = don't wait for the guest, only time the API
	ReadyTimeout time.Duration
	Poll         time.Duration // poll interval for socket / vsock probes
	CopyRootfs   bool          // copy rootfs per run (safe) vs symlink (fast, shares disk state)
	DropCaches   bool          // drop the host page cache before each run (needs root)
	Keep         bool          // keep per-run dirs (logs, metrics)
}

// Run is the measurement for a single restore. All durations are measured
// on the host with a monotonic clock.
type Run struct {
	Index int `json:"index"`
	// Spawn: exec(firecracker) -> API socket accepts a connection.
	Spawn time.Duration `json:"spawn_ns"`
	// Load: PUT /snapshot/load sent -> 2xx response (VM resumed).
	Load time.Duration `json:"load_ns"`
	// Ready: load returned -> guest agent answered over vsock.
	// This is where lazy page faulting of guest memory mostly lands.
	Ready time.Duration `json:"ready_ns"`
	// Total: exec -> guest ready.
	Total       time.Duration `json:"total_ns"`
	MinorFaults uint64        `json:"minor_faults"`
	MajorFaults uint64        `json:"major_faults"`
	// Raw "latencies_us" block from Firecracker's own metrics, if present.
	FCLatenciesUs map[string]any `json:"fc_latencies_us,omitempty"`
	Err           string         `json:"error,omitempty"`
}

// Bench restores cfg.SnapshotDir cfg.Runs times, each in a fresh Firecracker process.
func Bench(ctx context.Context, cfg Config) ([]Run, error) {
	snapDir, err := filepath.Abs(cfg.SnapshotDir)
	if err != nil {
		return nil, err
	}
	for _, f := range []string{StateName, MemName, RootfsName} {
		if _, err := os.Stat(filepath.Join(snapDir, f)); err != nil {
			return nil, fmt.Errorf("snapshot dir incomplete: %w", err)
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}

	runs := make([]Run, 0, cfg.Runs)
	for i := 1; i <= cfg.Runs; i++ {
		r := runOnce(ctx, cfg, snapDir, i)
		status := fmt.Sprintf("spawn=%s load=%s ready=%s total=%s",
			fmtDur(r.Spawn), fmtDur(r.Load), fmtDur(r.Ready), fmtDur(r.Total))
		if r.Err != "" {
			status = "ERROR: " + r.Err
		}
		fmt.Printf("run %3d/%d  %s\n", i, cfg.Runs, status)
		runs = append(runs, r)
		if ctx.Err() != nil {
			break
		}
	}
	return runs, nil
}

func runOnce(ctx context.Context, cfg Config, snapDir string, idx int) (r Run) {
	r.Index = idx
	runDir := filepath.Join(cfg.WorkDir, fmt.Sprintf("run-%04d", idx))
	_ = os.RemoveAll(runDir)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		r.Err = err.Error()
		return
	}
	if !cfg.Keep {
		defer os.RemoveAll(runDir)
	}

	// --- untimed setup ---
	rootfsDst := filepath.Join(runDir, RootfsName)
	var err error
	if cfg.CopyRootfs {
		err = copyFile(filepath.Join(snapDir, RootfsName), rootfsDst)
	} else {
		err = os.Symlink(filepath.Join(snapDir, RootfsName), rootfsDst)
	}
	if err != nil {
		r.Err = "prepare rootfs: " + err.Error()
		return
	}
	if cfg.DropCaches {
		if err := dropCaches(); err != nil {
			r.Err = "drop caches (need root?): " + err.Error()
			return
		}
	}

	// --- timed section ---
	t0 := time.Now()
	p, err := fc.Start(fc.ProcessConfig{
		Binary:  cfg.Firecracker,
		WorkDir: runDir,
		LogPath: filepath.Join(runDir, "firecracker.log"),
	})
	if err != nil {
		r.Err = err.Error()
		return
	}
	defer p.Kill()

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.WaitForSocket(sctx, cfg.Poll); err != nil {
		r.Err = err.Error()
		return
	}
	tSock := time.Now()

	// Metrics must be configured before load. This costs one small API call
	// and is excluded from the load phase.
	metricsPath := filepath.Join(runDir, "metrics.json")
	if f, err := os.Create(metricsPath); err == nil {
		f.Close()
	}
	if err := p.Client.PutMetrics(ctx, fc.Metrics{MetricsPath: metricsPath}); err != nil {
		r.Err = "metrics: " + err.Error()
		return
	}

	tLoadStart := time.Now()
	if err := p.Client.LoadSnapshot(ctx, fc.SnapshotLoad{
		SnapshotPath: filepath.Join(snapDir, StateName),
		MemBackend:   fc.MemBackend{BackendType: "File", BackendPath: filepath.Join(snapDir, MemName)},
		ResumeVM:     true,
	}); err != nil {
		r.Err = "snapshot/load: " + err.Error()
		return
	}
	tLoaded := time.Now()

	tReady := tLoaded
	if cfg.VsockPort != 0 {
		rctx, cancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
		defer cancel()
		if err := fc.ProbeVsock(rctx, filepath.Join(runDir, VsockName), cfg.VsockPort, cfg.Poll); err != nil {
			r.Err = err.Error()
			return
		}
		tReady = time.Now()
	}
	// --- end timed section ---

	r.Spawn = tSock.Sub(t0)
	r.Load = tLoaded.Sub(tLoadStart)
	r.Ready = tReady.Sub(tLoaded)
	r.Total = tReady.Sub(t0) - tLoadStart.Sub(tSock) // exclude the metrics call

	if minf, majf, err := p.PageFaults(); err == nil {
		r.MinorFaults, r.MajorFaults = minf, majf
	}
	if err := p.Client.FlushMetrics(ctx); err == nil {
		r.FCLatenciesUs = readLatencies(metricsPath)
	}
	return r
}

// readLatencies returns the "latencies_us" object from the last metrics line.
func readLatencies(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var last []byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
			last = append(last[:0], line...)
		}
	}
	var m map[string]any
	if json.Unmarshal(last, &m) != nil {
		return nil
	}
	lat, _ := m["latencies_us"].(map[string]any)
	return lat
}

func dropCaches() error {
	// sync first so dirty pages can actually be dropped.
	syscall.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o200)
}

// WriteJSON dumps raw per-run results.
func WriteJSON(path string, runs []Run) error {
	b, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
