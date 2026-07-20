package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ShuvoKumarMondal/firecracker-vms/internal/firecracker"
	"github.com/ShuvoKumarMondal/firecracker-vms/vm"
)

const (
	bridgeName = "br-fc"
	// Not 192.168.1.1/24: that collides with the typical home LAN, where .1
	// is usually the router. See vm.CheckSubnetConflict.
	bridgeIP = "10.200.0.1/24"
	// Grace given to a guest to power off after Ctrl+Alt+Del before its VMM is
	// force-killed. Shutdown returns as soon as the guest exits, so this is only
	// the wait for a guest that is slow or — like the bundled CI kernel, which
	// has no i8042 controller — ignores Ctrl+Alt+Del entirely.
	shutdownGrace = 5 * time.Second
)

func main() {
	count := flag.Int("n", 2, "number of microVMs to launch")
	flag.Parse()

	if err := run(*count); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(count int) error {
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

	taps, err := vm.SetupTapNetwork(bridgeName, count)
	if err != nil {
		return err
	}

	configs, err := vm.BuildConfigs(bridgeIP, taps)
	if err != nil {
		return err
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
			if err := m.Wait(); err != nil {
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

// stopAll asks each guest to power off, giving it a grace period before the VMM
// is killed. Shutdown blocks until each machine's process is gone.
func stopAll(machines []*firecracker.Machine) {
	var wg sync.WaitGroup
	for _, m := range machines {
		wg.Add(1)
		go func(m *firecracker.Machine) {
			defer wg.Done()
			if err := m.Shutdown(context.Background(), shutdownGrace); err != nil {
				log.Printf("%s: shutdown failed: %v", m.ID, err)
			}
		}(m)
	}
	wg.Wait()
}
