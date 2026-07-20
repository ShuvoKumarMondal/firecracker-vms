package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"

	"github.com/shuvo-14/firecracker-vms/vm"
)

const (
	bridgeName = "br-fc"
	// Not 192.168.1.1/24: that collides with the typical home LAN, where .1
	// is usually the router. See vm.CheckSubnetConflict.
	bridgeIP      = "10.200.0.1/24"
	shutdownGrace = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := vm.EnsureSudo(); err != nil {
		return err
	}

	if err := vm.CreateBridge(bridgeName, bridgeIP); err != nil {
		return err
	}
	// Torn down on every exit path, including errors.
	defer func() {
		if err := vm.Cleanup(bridgeName, bridgeIP); err != nil {
			log.Printf("cleanup: %v", err)
		}
	}()

	tap0, tap1, err := vm.SetupTapNetwork(bridgeName)
	if err != nil {
		return err
	}

	configs := []vm.VMConfig{
		{
			ID:         "vm1",
			SocketPath: "/tmp/firecracker1.sock",
			TapName:    tap0,
			MacAddress: "AA:BB:CC:00:00:01",
			IPAddress:  "10.200.0.2",
			BridgeIP:   bridgeIP,
		},
		{
			ID:         "vm2",
			SocketPath: "/tmp/firecracker2.sock",
			TapName:    tap1,
			MacAddress: "AA:BB:CC:00:00:02",
			IPAddress:  "10.200.0.3",
			BridgeIP:   bridgeIP,
		},
	}

	var machines []*firecracker.Machine
	for _, cfg := range configs {
		m, err := vm.StartFirecracker(ctx, cfg)
		if err != nil {
			stopAll(machines)
			return err
		}
		machines = append(machines, m)
	}

	log.Printf("%d microVMs running — press Ctrl+C to shut down", len(machines))
	for _, cfg := range configs {
		log.Printf("  %s  ssh -i resources/ubuntu-18.04.id_rsa root@%s", cfg.ID, cfg.IPAddress)
	}

	// Watch for the VMs terminating on their own, for example if a guest is
	// powered off from the inside.
	exited := make(chan struct{})
	var wg sync.WaitGroup
	for i, m := range machines {
		wg.Add(1)
		go func(id string, m *firecracker.Machine) {
			defer wg.Done()
			if err := m.Wait(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("%s exited: %v", id, err)
			}
		}(configs[i].ID, m)
	}
	go func() { wg.Wait(); close(exited) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case s := <-sigCh:
		log.Printf("received %s, shutting down", s)
		stopAll(machines)
	case <-exited:
		log.Print("all microVMs exited")
	}
	return nil
}

// stopAll asks each guest to power off, then makes sure the VMM processes are
// gone.
func stopAll(machines []*firecracker.Machine) {
	for _, m := range machines {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		if err := m.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
		cancel()
	}
	// Give the guests a moment to finish powering off before forcing the VMMs down.
	time.Sleep(2 * time.Second)
	for _, m := range machines {
		_ = m.StopVMM()
	}
}
