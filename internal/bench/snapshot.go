package bench

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"fcbench/internal/fc"
)

// Snapshot file names inside a snapshot directory. Resource paths are stored
// RELATIVE to the Firecracker process cwd, so a restore only needs a working
// directory that contains a file named RootfsName and room for VsockName.
const (
	RootfsName = "rootfs.ext4"
	VsockName  = "v.sock"
	StateName  = "vm.snap"
	MemName    = "vm.mem"
	GuestCID   = 3
)

type SnapshotConfig struct {
	Firecracker string
	Kernel      string
	Rootfs      string
	OutDir      string
	VCPUs       int
	MemMiB      int
	BootArgs    string
	VsockPort   uint32
	// If VsockPort is 0, we just sleep this long after boot before snapshotting.
	BootWait     time.Duration
	ReadyTimeout time.Duration
}

// CreateSnapshot boots a fresh VM, waits for the guest to be ready, pauses it
// and writes a full snapshot into cfg.OutDir.
func CreateSnapshot(ctx context.Context, cfg SnapshotConfig) error {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	kernel, err := filepath.Abs(cfg.Kernel)
	if err != nil {
		return err
	}

	// The snapshot's memory reflects the disk as it was at snapshot time, so
	// keep a private copy of the rootfs next to the snapshot.
	fmt.Printf("copying rootfs -> %s\n", filepath.Join(cfg.OutDir, RootfsName))
	if err := copyFile(cfg.Rootfs, filepath.Join(cfg.OutDir, RootfsName)); err != nil {
		return fmt.Errorf("copy rootfs: %w", err)
	}
	_ = os.Remove(filepath.Join(cfg.OutDir, VsockName))

	p, err := fc.Start(fc.ProcessConfig{
		Binary:  cfg.Firecracker,
		WorkDir: cfg.OutDir,
		LogPath: filepath.Join(cfg.OutDir, "snapshot-create.log"),
	})
	if err != nil {
		return err
	}
	defer p.Kill()

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.WaitForSocket(sctx, time.Millisecond); err != nil {
		return err
	}
	c := p.Client

	steps := []struct {
		name string
		fn   func() error
	}{
		{"boot-source", func() error {
			return c.PutBootSource(ctx, fc.BootSource{KernelImagePath: kernel, BootArgs: cfg.BootArgs})
		}},
		{"rootfs drive", func() error {
			return c.PutDrive(ctx, fc.Drive{DriveID: "rootfs", PathOnHost: RootfsName, IsRootDevice: true})
		}},
		{"machine-config", func() error {
			return c.PutMachineConfig(ctx, fc.MachineConfig{VCPUCount: cfg.VCPUs, MemSizeMiB: cfg.MemMiB})
		}},
		{"vsock", func() error {
			return c.PutVsock(ctx, fc.Vsock{GuestCID: GuestCID, UDSPath: VsockName})
		}},
		{"start", func() error { return c.StartInstance(ctx) }},
	}
	for _, s := range steps {
		if err := s.fn(); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	bootStart := time.Now()

	if cfg.VsockPort != 0 {
		fmt.Printf("waiting for guest agent on vsock port %d...\n", cfg.VsockPort)
		rctx, cancel := context.WithTimeout(ctx, cfg.ReadyTimeout)
		defer cancel()
		if err := fc.ProbeVsock(rctx, filepath.Join(p.WorkDir, VsockName), cfg.VsockPort, 50*time.Millisecond); err != nil {
			return fmt.Errorf("guest never became ready (see %s): %w", filepath.Join(cfg.OutDir, "snapshot-create.log"), err)
		}
		fmt.Printf("guest ready after cold boot: %v\n", time.Since(bootStart).Round(time.Millisecond))
	} else {
		fmt.Printf("no readiness probe; sleeping %v\n", cfg.BootWait)
		time.Sleep(cfg.BootWait)
	}

	if err := c.Pause(ctx); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	t := time.Now()
	if err := c.CreateSnapshot(ctx, fc.SnapshotCreate{
		SnapshotType: "Full",
		SnapshotPath: StateName,
		MemFilePath:  MemName,
	}); err != nil {
		return fmt.Errorf("snapshot/create: %w", err)
	}
	fmt.Printf("snapshot written to %s in %v\n", cfg.OutDir, time.Since(t).Round(time.Millisecond))
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
