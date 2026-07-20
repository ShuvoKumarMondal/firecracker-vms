package firecracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Binary is the Firecracker executable, resolved through PATH.
var Binary = "firecracker"

const (
	// apiTimeout bounds the wait for a freshly spawned VMM to serve its API.
	apiTimeout = 10 * time.Second
	// pollInterval is how often the API socket is probed while waiting.
	pollInterval = 10 * time.Millisecond
	// ctrlAltDelTimeout bounds the shutdown request itself, separately from
	// the grace period the guest then gets to act on it.
	ctrlAltDelTimeout = 5 * time.Second
)

// Config describes one microVM. Every path must be absolute: Firecracker
// resolves them relative to its own working directory, not the caller's.
type Config struct {
	ID         string
	SocketPath string

	// LogPath and MetricsPath receive the VMM's own output; ConsolePath
	// receives the guest's serial console. All three are optional.
	LogPath     string
	MetricsPath string
	ConsolePath string

	KernelImagePath string
	BootArgs        string
	RootDrivePath   string

	VcpuCount  int
	MemSizeMiB int

	TapDevice string
	GuestMAC  string
}

func (c Config) validate() error {
	switch {
	case c.ID == "":
		return errors.New("config: ID is required")
	case c.SocketPath == "":
		return errors.New("config: SocketPath is required")
	case c.KernelImagePath == "":
		return errors.New("config: KernelImagePath is required")
	case c.RootDrivePath == "":
		return errors.New("config: RootDrivePath is required")
	case c.VcpuCount <= 0:
		return fmt.Errorf("config: VcpuCount must be positive, got %d", c.VcpuCount)
	case c.MemSizeMiB <= 0:
		return fmt.Errorf("config: MemSizeMiB must be positive, got %d", c.MemSizeMiB)
	}
	return nil
}

// Machine is a running Firecracker process and the guest inside it.
type Machine struct {
	ID     string
	Client *Client

	cmd         *exec.Cmd
	socketPath  string
	consolePath string

	// done is closed once the VMM process has exited and been reaped;
	// waitErr is written before the close and read only after it.
	done    chan struct{}
	waitErr error
}

// Launch spawns a Firecracker process, configures the guest and boots it. The
// returned Machine is running; the caller must Wait on it or Shut it down.
func Launch(ctx context.Context, cfg Config) (*Machine, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(Binary); err != nil {
		return nil, fmt.Errorf("%s not found in PATH — run 'make setup' to install it", Binary)
	}

	// Firecracker refuses to start when its API socket already exists.
	if err := os.Remove(cfg.SocketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket %s: %w", cfg.SocketPath, err)
	}

	console, err := openConsole(cfg.ConsolePath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(Binary, "--api-sock", cfg.SocketPath, "--id", cfg.ID)
	cmd.Stdout = console
	cmd.Stderr = console
	// Give the VMM its own process group. A Ctrl+C in the terminal then reaches
	// only this launcher, which shuts guests down in order rather than having
	// them killed underneath it, and the group can later be killed as a unit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		console.Close()
		return nil, fmt.Errorf("start %s for %s: %w", Binary, cfg.ID, err)
	}

	m := &Machine{
		ID:          cfg.ID,
		Client:      NewClient(cfg.SocketPath),
		cmd:         cmd,
		socketPath:  cfg.SocketPath,
		consolePath: cfg.ConsolePath,
		done:        make(chan struct{}),
	}

	go func() {
		m.waitErr = cmd.Wait()
		if console != nil {
			console.Close()
		}
		// The socket is meaningless once the process backing it is gone, and
		// leaving it behind makes the next launch look like a stale-socket bug.
		os.Remove(cfg.SocketPath)
		close(m.done)
	}()

	if err := m.boot(ctx, cfg); err != nil {
		_ = m.Kill()
		return nil, err
	}
	return m, nil
}

// openConsole creates the guest serial console file. A nil result means the
// caller asked for no console, in which case the VMM's output is discarded.
func openConsole(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create console log %s: %w", path, err)
	}
	return f, nil
}

// boot applies the whole configuration and starts the guest. Order matters only
// in that logging and metrics must be set before InstanceStart, which locks the
// configuration for the life of the VM.
func (m *Machine) boot(ctx context.Context, cfg Config) error {
	if err := m.waitForAPI(ctx); err != nil {
		return err
	}

	if cfg.LogPath != "" {
		// Firecracker v1.7 requires the log and metrics files to exist already.
		// They are regular files, not FIFOs: a pipe with no reader blocks the
		// VMM as soon as the pipe buffer fills.
		if err := touch(cfg.LogPath); err != nil {
			return err
		}
		if err := m.Client.SetLogger(ctx, Logger{LogPath: cfg.LogPath, Level: "Info"}); err != nil {
			return err
		}
	}
	if cfg.MetricsPath != "" {
		if err := touch(cfg.MetricsPath); err != nil {
			return err
		}
		if err := m.Client.SetMetrics(ctx, Metrics{MetricsPath: cfg.MetricsPath}); err != nil {
			return err
		}
	}

	if err := m.Client.SetMachineConfig(ctx, MachineConfig{
		VcpuCount:  cfg.VcpuCount,
		MemSizeMib: cfg.MemSizeMiB,
		Smt:        false,
	}); err != nil {
		return err
	}

	if err := m.Client.SetBootSource(ctx, BootSource{
		KernelImagePath: cfg.KernelImagePath,
		BootArgs:        cfg.BootArgs,
	}); err != nil {
		return err
	}

	if err := m.Client.AddDrive(ctx, Drive{
		DriveID:      "rootfs",
		PathOnHost:   cfg.RootDrivePath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		return err
	}

	if cfg.TapDevice != "" {
		if err := m.Client.AddNetworkInterface(ctx, NetworkInterface{
			IfaceID:     "eth0",
			HostDevName: cfg.TapDevice,
			GuestMAC:    cfg.GuestMAC,
		}); err != nil {
			return err
		}
	}

	return m.Client.InstanceStart(ctx)
}

// waitForAPI blocks until the VMM is accepting API requests, or fails early if
// it exits first.
func (m *Machine) waitForAPI(ctx context.Context) error {
	deadline := time.Now().Add(apiTimeout)
	for {
		if conn, err := net.Dial("unix", m.socketPath); err == nil {
			conn.Close()
			return nil
		}

		select {
		case <-m.done:
			// Reporting "socket never appeared" here would hide the real cause,
			// which the VMM has already written to its console.
			return fmt.Errorf("%s: firecracker exited before its API was ready (%v); see %s",
				m.ID, m.waitErr, m.consolePath)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s: firecracker API socket %s not ready after %s",
				m.ID, m.socketPath, apiTimeout)
		}
	}
}

// Wait blocks until the VMM exits and reports why.
func (m *Machine) Wait() error {
	<-m.done
	return m.waitErr
}

// Exited reports whether the VMM has already stopped.
func (m *Machine) Exited() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

// Shutdown asks the guest to power off, gives it grace to do so, and kills the
// VMM if it does not. It returns once the process is gone.
func (m *Machine) Shutdown(ctx context.Context, grace time.Duration) error {
	if m.Exited() {
		return nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, ctrlAltDelTimeout)
	err := m.Client.SendCtrlAltDel(sendCtx)
	cancel()
	if err != nil {
		// Either the guest is already gone or it never booted far enough to
		// have a keyboard controller to press Ctrl+Alt+Del on.
		if killErr := m.Kill(); killErr != nil {
			return fmt.Errorf("%s: shutdown request failed (%v) and kill failed: %w", m.ID, err, killErr)
		}
		return nil
	}

	select {
	case <-m.done:
		return nil
	case <-time.After(grace):
		return m.Kill()
	}
}

// Kill terminates the VMM process group and waits for it to be reaped.
func (m *Machine) Kill() error {
	if m.Exited() {
		return nil
	}
	if m.cmd.Process != nil {
		// A negative pid signals the whole process group created by Setpgid.
		// ESRCH means it exited between the check above and this call.
		if err := syscall.Kill(-m.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill %s: %w", m.ID, err)
		}
	}
	<-m.done
	return nil
}

// touch creates a file if it does not exist, leaving existing content alone.
func touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return f.Close()
}
