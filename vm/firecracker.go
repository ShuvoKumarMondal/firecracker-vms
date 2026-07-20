package vm

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ShuvoKumarMondal/firecracker-vms/internal/firecracker"
)

// ResourcesDir holds the cached base images and the per-VM writable disks.
var ResourcesDir = "resources"

// Guest sizing. Small enough to run several VMs on a laptop; the guest kernel
// and base rootfs are the same for every VM.
const (
	vcpuCount  = 2
	memSizeMiB = 512
	kernelName = "vmlinux.bin"
	rootfsName = "ubuntu-18.04.ext4"
)

// cacheDir holds the read-only base images downloaded by 'make setup'.
func cacheDir() string { return filepath.Join(ResourcesDir, ".cache") }

// existingResource returns the absolute path to a resource, failing here rather
// than inside the Firecracker API if it is missing.
func existingResource(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %v", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("missing %s — run 'make setup' to download images", abs)
	}
	return abs, nil
}

// prepareRootfs returns a per-VM writable rootfs, copying it from the cached
// base image on first use. Each guest boots a read-write root device, so a
// shared image would be corrupted by concurrent writes.
func prepareRootfs(vmID string) (string, error) {
	dst, err := filepath.Abs(filepath.Join(ResourcesDir, vmID, rootfsName))
	if err != nil {
		return "", fmt.Errorf("resolve rootfs for %s: %v", vmID, err)
	}
	if _, err := os.Stat(dst); err == nil {
		return dst, nil // already provisioned by an earlier run
	}

	src, err := existingResource(filepath.Join(cacheDir(), rootfsName))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create resource dir for %s: %v", vmID, err)
	}
	// cp --reflink=auto is a near-instant copy-on-write clone on btrfs/xfs and a
	// normal copy elsewhere.
	if out, err := exec.Command("cp", "--reflink=auto", src, dst).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copy rootfs for %s: %v: %s", vmID, err, strings.TrimSpace(string(out)))
	}
	return dst, nil
}

// bootArgs builds the guest kernel command line, including the static network
// configuration Firecracker hands to the guest as ip=<client>::<gw>:<mask>...
// so the guest is addressed at boot with no DHCP server involved.
func bootArgs(cfg VMConfig) string {
	gateway := strings.Split(cfg.BridgeIP, "/")[0]
	// The netmask is derived from the bridge CIDR so it stays correct if the
	// subnet is changed from the default /24.
	netmask := "255.255.255.0"
	if _, ipNet, err := net.ParseCIDR(cfg.BridgeIP); err == nil {
		netmask = net.IP(ipNet.Mask).String()
	}
	// ip=<client-ip>::<gateway>:<netmask>::<iface>:off
	ipCfg := fmt.Sprintf("ip=%s::%s:%s::eth0:off", cfg.IPAddress, gateway, netmask)
	// No "ro": the root drive is attached writable, and the two must agree or
	// the guest mounts read-only regardless.
	return "console=ttyS0 reboot=k panic=1 pci=off " + ipCfg
}

// StartFirecracker boots a microVM described by cfg and returns the running
// machine, which the caller must wait on or shut down.
func StartFirecracker(ctx context.Context, cfg VMConfig) (*firecracker.Machine, error) {
	// The kernel is read-only and shared by every VM; the rootfs is a writable
	// per-VM copy.
	kernelPath, err := existingResource(filepath.Join(cacheDir(), kernelName))
	if err != nil {
		return nil, err
	}
	rootfsPath, err := prepareRootfs(cfg.ID)
	if err != nil {
		return nil, err
	}

	m, err := firecracker.Launch(ctx, firecracker.Config{
		ID:         cfg.ID,
		SocketPath: cfg.SocketPath,
		// Files, not FIFOs: a named pipe with no reader blocks the VMM once the
		// pipe buffer fills.
		LogPath:         cfg.SocketPath + ".log",
		MetricsPath:     cfg.SocketPath + "-metrics",
		ConsolePath:     cfg.SocketPath + ".console",
		KernelImagePath: kernelPath,
		BootArgs:        bootArgs(cfg),
		RootDrivePath:   rootfsPath,
		VcpuCount:       vcpuCount,
		MemSizeMiB:      memSizeMiB,
		TapDevice:       cfg.TapName,
		GuestMAC:        cfg.MacAddress,
	})
	if err != nil {
		return nil, err
	}

	log.Printf("microVM %s running: ip=%s tap=%s console=%s", cfg.ID, cfg.IPAddress, cfg.TapName, cfg.SocketPath+".console")
	return m, nil
}
