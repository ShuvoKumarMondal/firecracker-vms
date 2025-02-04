package vm

import (
	"fmt"
	"log"
	"os/exec"
)

// CreateBridge creates a bridge with the given name and IP address
func CreateBridge(bridgeName, bridgeIP string) error {
	cmd := exec.Command("sudo", "ip", "link", "add", bridgeName, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create bridge: %v", err)
	}

	cmd = exec.Command("sudo", "ip", "addr", "add", bridgeIP, "dev", bridgeName)
	if err := cmd.Run(); err != nil {
		cmd := exec.Command("sudo", "ip", "link", "delete", bridgeName)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to delete bridge after IP assignment failure: %v", err)
		}
		return fmt.Errorf("failed to assign IP address to bridge: %v", err)
	}

	cmd = exec.Command("sudo", "ip", "link", "set", bridgeName, "up")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring up bridge: %v", err)
	}

	cmd = exec.Command("sudo", "iptables", "-t", "nat", "-A", "POSTROUTING", "-o", bridgeName, "-j", "MASQUERADE")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set up the NAT rule for the bridge: %v", err)
	}

	cmd = exec.Command("sudo", "sysctl", "-w", "net.ipv4.ip_forward=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %v", err)
	}

	cmd = exec.Command("sudo", "iptables", "-A", "FORWARD", "--in-interface", bridgeName, "--out-interface", "wlo1", "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to allow forwarding from bridge to host's network interface: %v", err)
	}

	cmd = exec.Command("sudo", "iptables", "-A", "FORWARD", "--in-interface", "wlo1", "--out-interface", bridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to allow forwarding from host's network interface to bridge: %v", err)
	}

	log.Printf("Bridge %s created with IP %s", bridgeName, bridgeIP)
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
