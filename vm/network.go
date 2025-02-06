package vm

import (
	"fmt"
	"os/exec"
)

// createBridge creates a bridge with the given name and IP address
func CreateBridge(bridgeName string, ipAddress string) error {

	cmd := exec.Command("sudo", "ip", "link", "add", "name", bridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create bridge: %v", err)
	}

	cmd = exec.Command("sudo", "ip", "addr", "add", ipAddress, "dev", bridgeName)
	if err := cmd.Run(); err != nil {
		// If assigning IP address fails, we need to delete the bridge
		cmd := exec.Command("sudo", "ip", "link", "delete", bridgeName)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to delete bridge after IP assignment failure: %v", err)
		}
		return fmt.Errorf("failed to assign IP address to bridge: %v", err)
	}

	fmt.Printf("Bridge %s created and assigned IP Address %s\n", bridgeName, ipAddress)

	cmd = exec.Command("sudo", "ip", "link", "set", "dev", bridgeName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to up the bridge: %v", err)
	}

	cmd = exec.Command("sudo", "iptables", "-t", "nat", "-A", "POSTROUTING", "-o", bridgeName, "-j", "MASQUERADE")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to setup the NAT Rule to the bridge: %v", err)
	}

	// Enable IP forwarding
	cmd = exec.Command("sudo", "sysctl", "-w", "net.ipv4.ip_forward=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %v", err)
	}

	// Add a NAT rule for the host's network interface
	cmd = exec.Command("sudo", "iptables", "--table", "nat", "--append", "POSTROUTING", "--out-interface", "wlo1", "-j", "MASQUERADE")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add NAT rule for host's network interface: %v", err)
	}

	return nil
}

// createTap creates a tap with the given name and assigns it to the bridge
func createTap(tapName string, bridgeName string) error {
	cmd := exec.Command("sudo", "ip", "tuntap", "add", "dev", tapName, "mode", "tap")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create tap: %v", err)
	}

	cmd = exec.Command("sudo", "ip", "link", "set", "dev", tapName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up tap: %v", err)
	}

	cmd = exec.Command("sudo", "ip", "link", "set", "dev", tapName, "master", bridgeName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to assign tap to bridge: %v", err)
	}

	fmt.Printf("Tap %s assigned to Bridge %s\n", tapName, bridgeName)
	return nil
}

// SetupTapNetwork sets up the tap network with the given bridge name
func SetupTapNetwork(bridgeName string) (string, string, error) {
	fmt.Println("Setting up tap")

	tapName1 := bridgeName + "-tap" + "-0"
	tapName2 := bridgeName + "-tap" + "-1"

	if err := createTap(tapName1, bridgeName); err != nil {
		fmt.Println("Error creating tap for VM1:", err)
		return "", "", err
	}
	if err := createTap(tapName2, bridgeName); err != nil {
		fmt.Println("Error creating tap for VM2:", err)
		return "", "", err
	}
	return tapName1, tapName2, nil
}

// VMConfig holds the configuration details for a VM
type VMConfig struct {
	ID         string
	SocketPath string
	TapName    string
	MacAddress string
	IPAddress  string
	BridgeIP   string
}
