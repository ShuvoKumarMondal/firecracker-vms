package vm

import (
	"context"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
)

func StartFirecracker(vmID, socketPath, tapName, macAddress, ipAddr, bridgeIP string) error {
	ip, ipNet, err := net.ParseCIDR(ipAddr + "/24")
	if err != nil {
		return fmt.Errorf("invalid IP address: %s", ipAddr)
	}
	ipNet.IP = ip

	bridgeIPPart := strings.Split(bridgeIP, "/")[0]
	bridgeIPParsed := net.ParseIP(bridgeIPPart)
	if bridgeIPParsed == nil {
		return fmt.Errorf("invalid bridge IP address: %s", bridgeIP)
	}

	projectPath := "."
	kernelImagePath := filepath.Join(projectPath, "resources", "vmlinux.bin")
	rootfsPath := filepath.Join(projectPath, "resources", "rootfs.ext4")

	cfg := firecracker.Config{
		SocketPath:      socketPath,
		LogFifo:         socketPath + ".log",
		MetricsFifo:     socketPath + "-metrics",
		LogLevel:        "Debug",
		KernelImagePath: kernelImagePath,
		KernelArgs:      "ro console=ttyS0 reboot=k panic=1 pci=off",
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
					MacAddress:  macAddress,
					HostDevName: tapName,
					IPConfiguration: &firecracker.IPConfiguration{
						IPAddr:  *ipNet,
						Gateway: bridgeIPParsed,
					},
				},
			},
		},
	}

	log.Printf("Starting Firecracker VM with config: %+v\n", cfg)

	ctx := context.Background()
	m, err := firecracker.NewMachine(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create Firecracker VM: %v", err)
	}

	if err := m.Start(ctx); err != nil {
		return fmt.Errorf("failed to start Firecracker VM: %v", err)
	}

	log.Printf("Firecracker VM %s started with IP: %s\n", vmID, ipAddr)
	return nil
}
