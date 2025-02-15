package vm

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

func StartDockerContainer(vmID, imageName, tapName, macAddress, ipAddr, bridgeIP string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %v", err)
	}

	ctx := context.Background()

	// Create Docker network using our bridge
	networkName := "firecracker-net"
	_, err = cli.NetworkCreate(ctx, networkName, types.NetworkCreate{
		Driver: "bridge",
		Options: map[string]string{
			"com.docker.network.bridge.name": "br0",
		},
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to create network: %v", err)
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:        imageName,
		Tty:          true,  // Add TTY to keep container running
		AttachStdout: false, // Run in detached mode
		AttachStderr: false, // Run in detached mode
		Env: []string{
			fmt.Sprintf("IP_ADDRESS=%s", ipAddr),
			fmt.Sprintf("MAC_ADDRESS=%s", macAddress),
			fmt.Sprintf("TAP_DEVICE=%s", tapName),
			fmt.Sprintf("BRIDGE_IP=%s", bridgeIP),
		},
		Cmd: []string{
			"sh", "-c",
			fmt.Sprintf(`
                set -ex
                echo "Starting container setup..."
                
                # Install packages first (using default network)
                apt-get update
                DEBIAN_FRONTEND=noninteractive apt-get install -y iproute2 iputils-ping net-tools bridge-utils
                
                # Configure networking
                ip link set dev eth0 down
                ip addr flush dev eth0
                ip addr add %s/24 dev eth0
                ip link set dev eth0 address %s
                ip link set dev eth0 up
                
                # Update routing
                BRIDGE_IP=$(echo %s | cut -d'/' -f1)  # Remove subnet mask if present
                ip route show | grep -q "^default" && ip route del default || true
                ip route add default via $BRIDGE_IP dev eth0
                
                # Verify configuration
                echo "Network configuration:"
                ip addr show
                echo "Routing table:"
                ip route show
                
                # Keep container running
                tail -f /dev/null
            `, ipAddr, macAddress, bridgeIP),
		},
	}, &container.HostConfig{
		NetworkMode: container.NetworkMode("bridge"), // Use default bridge for initial setup
		Privileged:  true,
		CapAdd:      []string{"NET_ADMIN", "NET_RAW"},
	}, nil, nil, vmID)
	if err != nil {
		return fmt.Errorf("failed to create Docker container: %v", err)
	}

	// Start the container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start Docker container: %v", err)
	}

	// After container starts, connect it to our custom network
	if err := cli.NetworkConnect(ctx, networkName, resp.ID, &network.EndpointSettings{
		IPAddress:  ipAddr,
		MacAddress: macAddress,
	}); err != nil {
		return fmt.Errorf("failed to connect to custom network: %v", err)
	}

	fmt.Printf("Docker container %s started with IP: %s, MAC: %s, TAP: %s\n",
		vmID, ipAddr, macAddress, tapName)
	return nil
}
