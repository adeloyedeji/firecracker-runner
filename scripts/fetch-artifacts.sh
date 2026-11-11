#!/usr/bin/env bash
# Run INSIDE the Linux VM (Lima) or on the Linux host.
#
# Downloads the latest Firecracker release plus the Firecracker CI guest
# kernel and Ubuntu rootfs, bakes the fc-agent into the rootfs, and writes
# everything to $ARTIFACTS (default: ~/fc-artifacts).
#
# Requires: curl, wget, unsquashfs (squashfs-tools), mkfs.ext4 (e2fsprogs), sudo.
# Requires the guest agent to be built first: `make agent` (bin/fc-agent).
set -euo pipefail

ARCH="$(uname -m)"
ARTIFACTS="${ARTIFACTS:-$HOME/fc-artifacts}"
ROOTFS_SIZE="${ROOTFS_SIZE:-1G}"
AGENT_PORT="${AGENT_PORT:-1024}"
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
AGENT_BIN="${AGENT_BIN:-$REPO_DIR/bin/fc-agent-linux-$( [ "$ARCH" = aarch64 ] && echo arm64 || echo amd64 )}"
S3="https://s3.amazonaws.com/spec.ccfc.min"

[ -f "$AGENT_BIN" ] || { echo "missing $AGENT_BIN - run 'make agent' first"; exit 1; }
mkdir -p "$ARTIFACTS"
cd "$ARTIFACTS"

# ---- Firecracker binary ----
if [ ! -x firecracker ]; then
  release_url="https://github.com/firecracker-microvm/firecracker/releases"
  latest=$(basename "$(curl -fsSLI -o /dev/null -w '%{url_effective}' ${release_url}/latest)")
  echo ">> firecracker ${latest}"
  curl -fsSL "${release_url}/download/${latest}/firecracker-${latest}-${ARCH}.tgz" | tar -xz
  mv "release-${latest}-${ARCH}/firecracker-${latest}-${ARCH}" firecracker
  rm -rf "release-${latest}-${ARCH}"
fi
./firecracker --version | head -1

# ---- CI kernel + Ubuntu rootfs ----
CI_PREFIX=$(curl -fsSL "$S3?list-type=2&prefix=firecracker-ci/&delimiter=/" \
  | grep -oP "(?<=<Prefix>)firecracker-ci/[0-9]{8}-[^/]+/(?=</Prefix>)" | sort | tail -1)
echo ">> CI artifacts: $CI_PREFIX"

if ! ls vmlinux-* >/dev/null 2>&1; then
  kernel_key=$(curl -fsSL "$S3?list-type=2&prefix=${CI_PREFIX}${ARCH}/vmlinux-" \
    | grep -oP "(?<=<Key>)(${CI_PREFIX}${ARCH}/vmlinux-[0-9]+\.[0-9]+\.[0-9]{1,3})(?=</Key>)" | sort -V | tail -1)
  wget -q --show-progress "$S3/${kernel_key}"
fi
KERNEL=$(ls vmlinux-* | sort -V | tail -1)
ln -sf "$KERNEL" vmlinux

ubuntu_key=$(curl -fsSL "$S3?list-type=2&prefix=${CI_PREFIX}${ARCH}/ubuntu-" \
  | grep -oP "(?<=<Key>)(${CI_PREFIX}${ARCH}/ubuntu-[0-9]+\.[0-9]+\.squashfs)(?=</Key>)" | sort -V | tail -1)
ubuntu_version=$(basename "$ubuntu_key" .squashfs | grep -oE '[0-9]+\.[0-9]+')
SQUASH="ubuntu-$ubuntu_version.squashfs.upstream"
[ -f "$SQUASH" ] || wget -q --show-progress -O "$SQUASH" "$S3/$ubuntu_key"

# ---- Build rootfs with the agent baked in ----
echo ">> building rootfs.ext4 (ubuntu $ubuntu_version) with fc-agent on vsock port $AGENT_PORT"
sudo rm -rf squashfs-root
sudo unsquashfs -q "$SQUASH"

sudo install -m 0755 "$AGENT_BIN" squashfs-root/usr/local/bin/fc-agent
sudo tee squashfs-root/etc/systemd/system/fc-agent.service >/dev/null <<EOF
[Unit]
Description=fcbench readiness agent (vsock)
DefaultDependencies=no
After=local-fs.target

[Service]
ExecStart=/usr/local/bin/fc-agent -port ${AGENT_PORT}
Restart=always

[Install]
WantedBy=multi-user.target
EOF
sudo mkdir -p squashfs-root/etc/systemd/system/multi-user.target.wants
sudo ln -sf /etc/systemd/system/fc-agent.service \
  squashfs-root/etc/systemd/system/multi-user.target.wants/fc-agent.service

# SSH key for debugging the guest (optional).
[ -f id_rsa ] || ssh-keygen -q -f id_rsa -N ""
sudo mkdir -p squashfs-root/root/.ssh
sudo cp id_rsa.pub squashfs-root/root/.ssh/authorized_keys

sudo chown -R root:root squashfs-root
rm -f rootfs.ext4
truncate -s "$ROOTFS_SIZE" rootfs.ext4
sudo mkfs.ext4 -q -d squashfs-root -F rootfs.ext4
sudo chown "$(id -u):$(id -g)" rootfs.ext4
sudo rm -rf squashfs-root

echo
echo "artifacts in $ARTIFACTS:"
ls -lh "$ARTIFACTS" | grep -E 'firecracker|vmlinux|rootfs'
