package vm

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
)

// ResourcesDir holds the per-VM kernel and rootfs images.
var ResourcesDir = "resources"

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

// StartFirecracker boots a microVM described by cfg and returns the running
// machine, which the caller must wait on or shut down.
func StartFirecracker(ctx context.Context, cfg VMConfig) (*firecracker.Machine, error) {
	ip, ipNet, err := net.ParseCIDR(cfg.IPAddress + "/24")
	if err != nil {
		return nil, fmt.Errorf("invalid IP address %q: %v", cfg.IPAddress, err)
	}
	ipNet.IP = ip

	gateway := net.ParseIP(strings.Split(cfg.BridgeIP, "/")[0])
	if gateway == nil {
		return nil, fmt.Errorf("invalid bridge IP address: %s", cfg.BridgeIP)
	}

	kernelPath, err := resolve(cfg.ID, "vmlinux.bin")
	if err != nil {
		return nil, err
	}
	rootfsPath, err := resolve(cfg.ID, "ubuntu-18.04.ext4")
	if err != nil {
		return nil, err
	}

	// Firecracker refuses to start if the API socket already exists.
	if err := os.Remove(cfg.SocketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %s: %v", cfg.SocketPath, err)
	}

	fcCfg := firecracker.Config{
		VMID:       cfg.ID,
		SocketPath: cfg.SocketPath,
		// Files, not FIFOs: a named pipe with no reader blocks Firecracker
		// once the pipe buffer fills.
		LogPath:     cfg.SocketPath + ".log",
		MetricsPath: cfg.SocketPath + "-metrics",
		LogLevel:    "Info",

		KernelImagePath: kernelPath,
		// No "ro" here: the root drive is attached writable below, and the two
		// must agree or the guest mounts read-only regardless.
		KernelArgs: "console=ttyS0 reboot=k panic=1 pci=off",

		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(2),
			MemSizeMib: firecracker.Int64(512),
			Smt:        firecracker.Bool(false),
		},
		Drives: []models.Drive{
			{
				DriveID:      firecracker.String("rootfs"),
				PathOnHost:   firecracker.String(rootfsPath),
				IsRootDevice: firecracker.Bool(true),
				IsReadOnly:   firecracker.Bool(false),
			},
		},
		NetworkInterfaces: []firecracker.NetworkInterface{
			{
				StaticConfiguration: &firecracker.StaticNetworkConfiguration{
					MacAddress:  cfg.MacAddress,
					HostDevName: cfg.TapName,
					IPConfiguration: &firecracker.IPConfiguration{
						IPAddr:  *ipNet,
						Gateway: gateway,
					},
				},
			},
		},
	}

	// The SDK's default command builder wires firecracker to os.Stdout/Stderr,
	// which puts the guest's serial console on our terminal. Redirect to a
	// per-VM file; it stays open for the lifetime of the VM.
	consolePath := cfg.SocketPath + ".console"
	console, err := os.Create(consolePath)
	if err != nil {
		return nil, fmt.Errorf("create console log %s: %v", consolePath, err)
	}

	cmd := firecracker.VMCommandBuilder{}.
		WithBin("firecracker").
		WithSocketPath(cfg.SocketPath).
		WithStdout(console).
		WithStderr(console).
		Build(ctx)

	// The SDK logs every API call at info level. Only surface warnings.
	sdkLog := logrus.New()
	sdkLog.SetLevel(logrus.WarnLevel)

	m, err := firecracker.NewMachine(ctx, fcCfg,
		firecracker.WithProcessRunner(cmd),
		firecracker.WithLogger(logrus.NewEntry(sdkLog)),
	)
	if err != nil {
		console.Close()
		return nil, fmt.Errorf("create microVM %s: %v", cfg.ID, err)
	}

	if err := m.Start(ctx); err != nil {
		console.Close()
		return nil, fmt.Errorf("start microVM %s: %v", cfg.ID, err)
	}

	log.Printf("microVM %s running: ip=%s tap=%s console=%s", cfg.ID, cfg.IPAddress, cfg.TapName, consolePath)
	return m, nil
}
