// Command fcbench creates Firecracker snapshots and benchmarks restoring them.
//
//	fcbench snapshot --kernel vmlinux --rootfs rootfs.ext4 --out ./snap
//	fcbench bench    --snapshot ./snap --runs 50 --json results.json
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"time"

	"fcbench/internal/bench"
)

func defaultBootArgs() string {
	args := "console=ttyS0 reboot=k panic=1"
	if runtime.GOARCH == "arm64" {
		args = "keep_bootcon " + args
	}
	return args
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch os.Args[1] {
	case "snapshot":
		err = cmdSnapshot(ctx, os.Args[2:])
	case "bench":
		err = cmdBench(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fcbench - Firecracker snapshot restore benchmark

Commands:
  snapshot   boot a VM from kernel+rootfs and write a full snapshot
  bench      restore a snapshot N times and report per-phase timings

Run "fcbench <command> -h" for flags.
`)
}

func cmdSnapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	var c bench.SnapshotConfig
	var port uint
	fs.StringVar(&c.Firecracker, "firecracker", "./firecracker", "path to firecracker binary")
	fs.StringVar(&c.Kernel, "kernel", "", "guest kernel (uncompressed vmlinux)")
	fs.StringVar(&c.Rootfs, "rootfs", "", "guest root filesystem (ext4)")
	fs.StringVar(&c.OutDir, "out", "./snap", "output snapshot directory")
	fs.IntVar(&c.VCPUs, "vcpus", 1, "vCPU count")
	fs.IntVar(&c.MemMiB, "mem", 256, "guest memory in MiB")
	fs.StringVar(&c.BootArgs, "boot-args", defaultBootArgs(), "kernel command line")
	fs.UintVar(&port, "vsock-port", 1024, "guest agent vsock port to wait on before snapshotting (0 = just sleep)")
	fs.DurationVar(&c.BootWait, "boot-wait", 5*time.Second, "sleep after boot when -vsock-port=0")
	fs.DurationVar(&c.ReadyTimeout, "ready-timeout", 60*time.Second, "max time to wait for guest agent")
	fs.Parse(args)
	if c.Kernel == "" || c.Rootfs == "" {
		fs.Usage()
		return fmt.Errorf("-kernel and -rootfs are required")
	}
	c.VsockPort = uint32(port)
	return bench.CreateSnapshot(ctx, c)
}

func cmdBench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	var c bench.Config
	var port uint
	var jsonOut string
	fs.StringVar(&c.Firecracker, "firecracker", "./firecracker", "path to firecracker binary")
	fs.StringVar(&c.SnapshotDir, "snapshot", "./snap", "snapshot directory produced by 'fcbench snapshot'")
	fs.StringVar(&c.WorkDir, "workdir", "./runs", "scratch dir for per-run working directories")
	fs.IntVar(&c.Runs, "runs", 20, "number of restores")
	fs.UintVar(&port, "vsock-port", 1024, "guest agent vsock port for readiness (0 = time API only)")
	fs.DurationVar(&c.ReadyTimeout, "ready-timeout", 10*time.Second, "max time to wait for guest readiness")
	fs.DurationVar(&c.Poll, "poll", 200*time.Microsecond, "poll interval for socket and vsock probes")
	fs.BoolVar(&c.CopyRootfs, "copy-rootfs", true, "copy rootfs per run (false = symlink, faster but runs share disk state)")
	fs.BoolVar(&c.DropCaches, "drop-caches", false, "drop host page cache before each run for cold numbers (root)")
	fs.BoolVar(&c.Keep, "keep", false, "keep per-run directories (logs, metrics)")
	fs.StringVar(&jsonOut, "json", "", "write raw per-run results to this file")
	fs.Parse(args)
	c.VsockPort = uint32(port)

	runs, err := bench.Bench(ctx, c)
	if err != nil {
		return err
	}
	bench.PrintTable(os.Stdout, runs)
	if jsonOut != "" {
		if err := bench.WriteJSON(jsonOut, runs); err != nil {
			return err
		}
		fmt.Println("raw results:", jsonOut)
	}
	return nil
}
