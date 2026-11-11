# fcbench

Boot a Firecracker microVM from a snapshot and measure how long startup takes,
phase by phase.

```
phase                    min     p50     p90     p99     max     avg
spawn -> api socket      ...
snapshot/load (API)      ...
resume -> guest ready    ...
TOTAL                    ...
```

| Phase | Measured from → to |
|---|---|
| spawn → api socket | `exec(firecracker)` → API Unix socket accepts a connection |
| snapshot/load (API) | `PUT /snapshot/load` sent (with `resume_vm: true`) → 2xx |
| resume → guest ready | load returned → **guest userspace** answers on vsock |
| TOTAL | exec → guest ready (excludes the one untimed `PUT /metrics` call) |

It also records the Firecracker process's minor and major page faults per run,
and Firecracker's own `latencies_us` metrics. Raw results go to `-json`.

## Layout

```
cmd/fcbench/        host CLI: `snapshot` and `bench` subcommands
cmd/fc-agent/       tiny guest agent: listens on vsock, replies "ready\n"
internal/fc/        Firecracker API client (Unix socket), process mgmt, vsock probe
internal/bench/     snapshot creation, benchmark loop, stats
scripts/fetch-artifacts.sh   gets firecracker + CI kernel + Ubuntu rootfs, bakes agent in
lima/firecracker.yaml        Ubuntu VM with nested virtualization for your M5
```

Standard library only, no third-party modules.

## Quick start (M5 Mac)

**1. Create the Linux VM** (macOS 15+, `brew install lima`):

```sh
cd ~/src/repos/virtualization
limactl start --name=fc ./lima/firecracker.yaml
limactl shell fc
ls -l /dev/kvm            # must exist, otherwise nested virt isn't on
```

**2. Build** (on the Mac with Go installed, or inside the VM):

```sh
make            # -> bin/fcbench-linux-arm64, bin/fc-agent-linux-arm64
make test
```

**3. Inside the VM**, from `~/src/repos/virtualization`:

```sh
make artifacts  # firecracker, vmlinux, rootfs.ext4 (with fc-agent) -> ~/fc-artifacts
make snapshot   # cold-boots the VM, waits for fc-agent, pauses, writes ~/fc-artifacts/snap
make bench RUNS=50
make bench-cold RUNS=20   # drops the page cache before each run (sudo)
```

Keep artifacts and snapshots on the VM's own disk (`~/fc-artifacts`), not on
the shared Mac folder. virtiofs slows down snapshot memory loading and would
distort the numbers.

## How it works

**Snapshot** (`fcbench snapshot`): copies the rootfs into the snapshot dir,
starts Firecracker with that dir as its cwd, configures boot source, drive,
machine config and vsock with **relative** paths (`rootfs.ext4`, `v.sock`),
boots, waits until `fc-agent` answers, pauses, and calls `PUT /snapshot/create`
→ `vm.snap` + `vm.mem`.

**Bench** (`fcbench bench`): for each run, creates a fresh run dir, copies the
rootfs into it (untimed; `-copy-rootfs=false` symlinks instead), optionally
drops caches, then times: spawn a new Firecracker in that dir → socket ready →
`PUT /snapshot/load` → vsock probe. Because snapshot resource paths are
relative, every run gets its own disk and vsock socket without path overrides.

**Readiness**: Firecracker's vsock exposes guest ports through a host Unix
socket. The host connects, sends `CONNECT 1024\n`, gets `OK …`, and then
`fc-agent` writes `ready`. Listening vsock sockets survive snapshot/restore
(open connections are reset), so this works immediately after resume. Use
`-vsock-port 0` to time only the API.

## Things that affect your numbers

- **Nested virtualization.** Lima on the Mac is fine for development and
  relative comparisons. For real numbers, run on bare-metal Linux (e.g. an
  AWS `.metal` instance).
- **Page cache.** With the `File` memory backend, guest memory is `mmap`ed and
  faulted in lazily, so most of the cost lands in "resume → guest ready".
  Compare `make bench` (warm) against `make bench-cold`.
- **Memory size.** Try `-mem 128` vs `-mem 1024` at snapshot time.
- **Same-host rules.** Restore needs the same Firecracker version and a
  compatible CPU. Re-run `make snapshot` after upgrading Firecracker.
- **Clones are identical.** Every restore has the same RNG state, MAC and
  clock. That's fine for benchmarks, but matters in production.

## Next steps

- Add a `Uffd` memory backend (a userfaultfd page server) to control and
  measure memory loading.
- Network readiness: a tap device plus an HTTP probe against a guest app.
- Run it under the `jailer` for production-like numbers.
- Diff snapshots (`track_dirty_pages`) and snapshot-create timing.
