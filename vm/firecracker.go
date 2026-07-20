package vm

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShuvoKumarMondal/firecracker-vms/internal/firecracker"
)

// ResourcesDir holds the per-VM kernel and rootfs images.
var ResourcesDir = "resources"

// Guest sizing. Small enough to run several VMs on a laptop; the guest kernel
// and rootfs are the same for every VM.
const (
	vcpuCount  = 2
	memSizeMiB = 512
	kernelName = "vmlinux.bin"
	rootfsName = "ubuntu-18.04.ext4"
)

// resolve returns the absolute path to a VM resource and verifies it exists,
// so a missing image fails here rather than inside the Firecracker API.
func resolve(vmID, name string) (string, error) {
	path, err := filepath.Abs(filepath.Join(ResourcesDir, vmID, name))
	if err != nil {
		return "", fmt.Errorf("resolve %s for %s: %v", name, vmID, err)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("missing %s for %s at %s — run 'make setup' to download images", name, vmID, path)
	}
	return path, nil
}

// bootArgs builds the guest kernel command line, including the static network
// configuration Firecracker hands to the guest as ip=<client>::<gw>:<mask>...
// so the guest is addressed at boot with no DHCP server involved.
func bootArgs(cfg VMConfig) string {
	gateway := strings.Split(cfg.BridgeIP, "/")[0]
	// ip=<client-ip>::<gateway>:<netmask>::<iface>:off
	ipCfg := fmt.Sprintf("ip=%s::%s:255.255.255.0::eth0:off", cfg.IPAddress, gateway)
	// No "ro": the root drive is attached writable, and the two must agree or
	// the guest mounts read-only regardless.
	return "console=ttyS0 reboot=k panic=1 pci=off " + ipCfg
}

// StartFirecracker boots a microVM described by cfg and returns the running
// machine, which the caller must wait on or shut down.
func StartFirecracker(ctx context.Context, cfg VMConfig) (*firecracker.Machine, error) {
	kernelPath, err := resolve(cfg.ID, kernelName)
	if err != nil {
		return nil, err
	}
	rootfsPath, err := resolve(cfg.ID, rootfsName)
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
