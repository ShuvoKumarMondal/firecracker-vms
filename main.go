package main

import (
	"fmt"
	"log"
	"time"

	"github.com/shuvo-14/firecracker-vms/vm"
)

func main() {
	// Create Bridge and TAP devices
	bridgeIP := "192.168.1.1/24"
	bridgeName := "br0"

	// Create Bridge
	if err := vm.CreateBridge(bridgeName, bridgeIP); err != nil {
		log.Fatalf("Failed to create bridge: %v", err)
	}

	// Setup TAP network
	tap0, tap1, err := vm.SetupTapNetwork(bridgeName)
	if err != nil {
		log.Fatalf("Failed to set up TAP network: %v", err)
	}

	// Wait for bridge to be ready
	time.Sleep(2 * time.Second)

	// Define VM configurations
	vm1 := vm.VMConfig{
		ID:         "vm1",
		SocketPath: "/tmp/firecracker1.sock",
		TapName:    tap0,
		MacAddress: "AA:BB:CC:00:00:01",
		IPAddress:  "192.168.1.2",
		BridgeIP:   bridgeIP,
	}

	vm2 := vm.VMConfig{
		ID:         "vm2",
		SocketPath: "/tmp/firecracker2.sock",
		TapName:    tap1,
		MacAddress: "AA:BB:CC:00:00:02",
		IPAddress:  "192.168.1.3",
		BridgeIP:   bridgeIP,
	}

	// Start Docker containers
	imageName := "ubuntu:18.04"

	fmt.Println("Starting Docker container for VM1...")
	if err := vm.StartDockerContainer(vm1.ID, imageName, vm1.TapName, vm1.MacAddress, vm1.IPAddress, vm1.BridgeIP); err != nil {
		log.Fatalf("Failed to start Docker container for VM1: %v", err)
	}

	fmt.Println("Starting Docker container for VM2...")
	if err := vm.StartDockerContainer(vm2.ID, imageName, vm2.TapName, vm2.MacAddress, vm2.IPAddress, vm2.BridgeIP); err != nil {
		log.Fatalf("Failed to start Docker container for VM2: %v", err)
	}

	fmt.Println("Both Docker containers are running with network configured!")

	// Example usage of SocketPath
	fmt.Printf("VM1 SocketPath: %s\n", vm1.SocketPath)
	fmt.Printf("VM2 SocketPath: %s\n", vm2.SocketPath)
}
