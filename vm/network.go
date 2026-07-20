package vm

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// run executes a command, returning an error that carries the command's own
// stderr. Without it, ip and iptables failures surface only as "exit status 1".
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
}

func sudo(args ...string) error { return run("sudo", args...) }

// EnsureSudo checks that sudo can run without prompting. Privileged commands
// capture their own output, so a password prompt has nowhere to render and
// would fail opaquely.
func EnsureSudo() error {
	if exec.Command("sudo", "-n", "true").Run() == nil {
		return nil
	}
	return fmt.Errorf("sudo needs a password here; run 'sudo -v' first, then start again")
}

// linkExists reports whether a network interface with the given name is present.
func linkExists(name string) bool {
	return exec.Command("ip", "link", "show", name).Run() == nil
}

// addrAssigned reports whether cidr is already assigned to the given link.
func addrAssigned(link, cidr string) bool {
	out, err := exec.Command("ip", "-o", "addr", "show", "dev", link).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), cidr)
}

// DefaultInterface returns the host interface carrying the default route,
// which is where guest traffic must be masqueraded.
func DefaultInterface() (string, error) {
	out, err := exec.Command("ip", "route", "get", "8.8.8.8").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("determine default interface: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("no default route interface found in %q", strings.TrimSpace(string(out)))
}

// SubnetOf converts a bridge address such as "192.168.1.1/24" into the network
// it belongs to, e.g. "192.168.1.0/24". NAT and forwarding rules are scoped to
// this subnet rather than applied to all traffic on the uplink.
func SubnetOf(bridgeCIDR string) (string, error) {
	_, ipNet, err := net.ParseCIDR(bridgeCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid bridge CIDR %q: %v", bridgeCIDR, err)
	}
	return ipNet.String(), nil
}

// CheckSubnetConflict reports whether the bridge subnet overlaps one already
// present on this host.
//
// 192.168.1.0/24 is the obvious choice for a guest network and the worst one:
// it is the most common home LAN, and .1 is usually the router. Assigning it
// to a bridge installs a duplicate route and sends gateway-bound traffic to
// the bridge, taking the host off the network — and none of the underlying
// ip or iptables commands report an error.
func CheckSubnetConflict(bridgeName, bridgeCIDR string) error {
	ip, want, err := net.ParseCIDR(bridgeCIDR)
	if err != nil {
		return fmt.Errorf("invalid bridge CIDR %q: %v", bridgeCIDR, err)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("list host interfaces: %v", err)
	}

	for _, iface := range ifaces {
		// Skip our own bridge, which may survive from an earlier run.
		if iface.Name == bridgeName {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			existing, ok := addr.(*net.IPNet)
			if !ok || existing.IP.To4() == nil {
				continue
			}
			if want.Contains(existing.IP) || existing.Contains(ip) {
				return fmt.Errorf(
					"bridge subnet %s overlaps %s already configured on %s; "+
						"choose a subnet that is not in use on this host",
					bridgeCIDR, existing.String(), iface.Name)
			}
		}
	}
	return nil
}

// ensureRule appends an iptables rule only if an identical one is absent, so
// repeated runs do not accumulate duplicates.
func ensureRule(table string, spec ...string) error {
	check := append([]string{"iptables", "-t", table, "-C"}, spec...)
	if sudo(check...) == nil {
		return nil // already present
	}
	add := append([]string{"iptables", "-t", table, "-A"}, spec...)
	return sudo(add...)
}

// deleteRule removes a rule if present, ignoring "no such rule" errors.
func deleteRule(table string, spec ...string) {
	check := append([]string{"iptables", "-t", table, "-C"}, spec...)
	if sudo(check...) != nil {
		return
	}
	del := append([]string{"iptables", "-t", table, "-D"}, spec...)
	_ = sudo(del...)
}

// CreateBridge creates the bridge, assigns its address, enables forwarding and
// installs NAT. It is safe to call repeatedly: every step checks for the state
// it intends to create before creating it.
func CreateBridge(bridgeName, ipAddress string) error {
	subnet, err := SubnetOf(ipAddress)
	if err != nil {
		return err
	}

	if err := CheckSubnetConflict(bridgeName, ipAddress); err != nil {
		return err
	}

	uplink, err := DefaultInterface()
	if err != nil {
		return err
	}

	createdBridge := false
	if !linkExists(bridgeName) {
		if err := sudo("ip", "link", "add", "name", bridgeName, "type", "bridge"); err != nil {
			return fmt.Errorf("failed to create bridge: %v", err)
		}
		createdBridge = true
	}

	if !addrAssigned(bridgeName, ipAddress) {
		if err := sudo("ip", "addr", "add", ipAddress, "dev", bridgeName); err != nil {
			// Roll back only a bridge we created ourselves, and surface the
			// address failure rather than the cleanup failure.
			if createdBridge {
				if delErr := sudo("ip", "link", "delete", bridgeName); delErr != nil {
					return fmt.Errorf("failed to assign IP address to bridge: %v (cleanup also failed: %v)", err, delErr)
				}
			}
			return fmt.Errorf("failed to assign IP address to bridge: %v", err)
		}
	}

	if err := sudo("ip", "link", "set", "dev", bridgeName, "up"); err != nil {
		return fmt.Errorf("failed to bring up the bridge: %v", err)
	}

	if err := sudo("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %v", err)
	}

	// Masquerade on the uplink, not the bridge: a MASQUERADE rule on the bridge
	// rewrites host-to-guest traffic instead, hiding real source addresses.
	if err := ensureRule("nat", "POSTROUTING", "-s", subnet, "-o", uplink, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("failed to add NAT rule for %s: %v", uplink, err)
	}

	// Explicit FORWARD rules. Docker sets the FORWARD policy to DROP, so
	// without these the guests get working NAT and still cannot reach anything.
	if err := ensureRule("filter", "FORWARD", "-i", bridgeName, "-o", uplink, "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("failed to allow forwarding out of %s: %v", bridgeName, err)
	}
	if err := ensureRule("filter", "FORWARD", "-i", uplink, "-o", bridgeName,
		"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return fmt.Errorf("failed to allow return traffic into %s: %v", bridgeName, err)
	}

	fmt.Printf("Bridge %s is up with address %s, NAT via %s\n", bridgeName, ipAddress, uplink)
	return nil
}

// createTap creates a tap device and enslaves it to the bridge. Idempotent.
func createTap(tapName, bridgeName string) error {
	if !linkExists(tapName) {
		if err := sudo("ip", "tuntap", "add", "dev", tapName, "mode", "tap"); err != nil {
			return fmt.Errorf("failed to create tap: %v", err)
		}
	}
	if err := sudo("ip", "link", "set", "dev", tapName, "up"); err != nil {
		return fmt.Errorf("failed to bring up tap: %v", err)
	}
	if err := sudo("ip", "link", "set", "dev", tapName, "master", bridgeName); err != nil {
		return fmt.Errorf("failed to assign tap to bridge: %v", err)
	}
	fmt.Printf("Tap %s attached to bridge %s\n", tapName, bridgeName)
	return nil
}

// TapNames returns the deterministic tap device names for the bridge.
func TapNames(bridgeName string) []string {
	return []string{bridgeName + "-tap-0", bridgeName + "-tap-1"}
}

// SetupTapNetwork creates the tap devices used by the two VMs.
func SetupTapNetwork(bridgeName string) (string, string, error) {
	taps := TapNames(bridgeName)
	for i, tap := range taps {
		if err := createTap(tap, bridgeName); err != nil {
			return "", "", fmt.Errorf("tap for VM%d: %v", i+1, err)
		}
	}
	return taps[0], taps[1], nil
}

// Cleanup removes every piece of host state CreateBridge and SetupTapNetwork
// created: tap devices, NAT and forwarding rules, and the bridge itself.
func Cleanup(bridgeName, ipAddress string) error {
	var problems []string

	for _, tap := range TapNames(bridgeName) {
		if linkExists(tap) {
			if err := sudo("ip", "link", "delete", tap); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}

	if subnet, err := SubnetOf(ipAddress); err == nil {
		if uplink, err := DefaultInterface(); err == nil {
			deleteRule("nat", "POSTROUTING", "-s", subnet, "-o", uplink, "-j", "MASQUERADE")
			deleteRule("filter", "FORWARD", "-i", bridgeName, "-o", uplink, "-j", "ACCEPT")
			deleteRule("filter", "FORWARD", "-i", uplink, "-o", bridgeName,
				"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		}
	}

	if linkExists(bridgeName) {
		if err := sudo("ip", "link", "delete", bridgeName); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("cleanup incomplete: %s", strings.Join(problems, "; "))
	}
	fmt.Printf("Removed bridge %s, its taps and firewall rules\n", bridgeName)
	return nil
}

// VMConfig holds the configuration details for a VM.
type VMConfig struct {
	ID         string
	SocketPath string
	TapName    string
	MacAddress string
	IPAddress  string
	BridgeIP   string
}
