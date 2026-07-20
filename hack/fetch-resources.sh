#!/usr/bin/env bash
#
# Downloads the kernel, base root filesystem and SSH key into resources/.cache,
# and installs the Firecracker binary if it is missing.
#
# The images are cached read-only. The launcher shares the kernel across all
# VMs and makes a writable per-VM copy of the rootfs on demand, so the number of
# VMs is chosen at run time (-n) rather than fixed here.

set -euo pipefail

FIRECRACKER_VERSION="${FIRECRACKER_VERSION:-v1.7.0}"

KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.11/x86_64/vmlinux-5.10.225"
ROOTFS_URL="https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/disks/x86_64/ubuntu-18.04.ext4"
SSHKEY_URL="https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/disks/x86_64/ubuntu-18.04.id_rsa"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESOURCES="${REPO_ROOT}/resources"
CACHE="${RESOURCES}/.cache"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
err() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; }

# Scratch directory for the firecracker tarball, cleaned up on exit. This is
# deliberately script-scoped rather than a function local with a RETURN trap:
# the trap body is evaluated after the function's locals are gone, which under
# `set -u` fails with "tmp: unbound variable".
SCRATCH=""
cleanup_scratch() {
  if [[ -n "${SCRATCH}" && -d "${SCRATCH}" ]]; then
    rm -rf "${SCRATCH}"
  fi
  # Must return success: the exit status of an EXIT trap becomes the script's
  # own exit status, so a falsy final test here would fail an otherwise
  # successful run.
  return 0
}
trap cleanup_scratch EXIT

# fetch <url> <destination> — downloads only if absent, resuming partial files.
fetch() {
  local url="$1" dest="$2"
  if [[ -s "${dest}" ]]; then
    log "cached  $(basename "${dest}")"
    return
  fi
  log "fetching $(basename "${dest}")"
  curl -fL --retry 3 --continue-at - --progress-bar -o "${dest}.partial" "${url}"
  mv "${dest}.partial" "${dest}"
}

# Record checksums on first download and verify them on every later run, so a
# truncated or tampered image is caught rather than surfacing as a boot failure.
verify_checksums() {
  local sums="${CACHE}/SHA256SUMS"
  cd "${CACHE}"
  if [[ -f "${sums}" ]]; then
    log "verifying checksums"
    if ! sha256sum --quiet --check "${sums}"; then
      err "checksum mismatch — delete ${CACHE} and re-run to refetch"
      exit 1
    fi
  else
    log "recording checksums"
    sha256sum vmlinux.bin ubuntu-18.04.ext4 ubuntu-18.04.id_rsa > "${sums}"
  fi
  cd - >/dev/null
}

install_firecracker() {
  if command -v firecracker >/dev/null 2>&1; then
    log "firecracker already installed: $(firecracker --version | head -1)"
    return
  fi

  log "installing firecracker ${FIRECRACKER_VERSION}"
  local arch
  arch="$(uname -m)"
  SCRATCH="$(mktemp -d)"

  curl -fL --retry 3 --progress-bar \
    -o "${SCRATCH}/firecracker.tgz" \
    "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-${arch}.tgz"
  tar -xzf "${SCRATCH}/firecracker.tgz" -C "${SCRATCH}"

  log "installing to /usr/local/bin (requires sudo)"
  sudo install -m 0755 \
    "${SCRATCH}/release-${FIRECRACKER_VERSION}-${arch}/firecracker-${FIRECRACKER_VERSION}-${arch}" \
    /usr/local/bin/firecracker
  log "installed: $(firecracker --version | head -1)"
}

main() {
  if [[ ! -e /dev/kvm ]]; then
    err "/dev/kvm not present — this host cannot run Firecracker"
    exit 1
  fi
  if [[ ! -r /dev/kvm || ! -w /dev/kvm ]]; then
    err "/dev/kvm is not readable/writable by $(id -un)"
    err "fix with: sudo usermod -aG kvm $(id -un)   (then log out and back in)"
    exit 1
  fi

  mkdir -p "${CACHE}"
  fetch "${KERNEL_URL}" "${CACHE}/vmlinux.bin"
  fetch "${ROOTFS_URL}" "${CACHE}/ubuntu-18.04.ext4"
  fetch "${SSHKEY_URL}" "${CACHE}/ubuntu-18.04.id_rsa"
  verify_checksums

  install -m 0600 "${CACHE}/ubuntu-18.04.id_rsa" "${RESOURCES}/ubuntu-18.04.id_rsa"
  install_firecracker

  log "ready — run 'make run' (or 'make run N=5' for five VMs)"
}

main "$@"
