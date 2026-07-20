package vm

import (
	"net"
	"testing"
)

func TestSubnetOf(t *testing.T) {
	tests := []struct {
		name      string
		bridgeIP  string
		want      string
		wantError bool
	}{
		{name: "class C /24", bridgeIP: "192.168.1.1/24", want: "192.168.1.0/24"},
		{name: "already a network address", bridgeIP: "10.200.0.0/24", want: "10.200.0.0/24"},
		{name: "non-octet boundary", bridgeIP: "172.16.5.9/20", want: "172.16.0.0/20"},
		{name: "single host", bridgeIP: "10.0.0.1/32", want: "10.0.0.1/32"},
		{name: "missing prefix", bridgeIP: "192.168.1.1", wantError: true},
		{name: "empty", bridgeIP: "", wantError: true},
		{name: "garbage", bridgeIP: "not-an-address", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SubnetOf(tc.bridgeIP)
			if tc.wantError {
				if err == nil {
					t.Fatalf("SubnetOf(%q) = %q, want an error", tc.bridgeIP, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SubnetOf(%q) returned unexpected error: %v", tc.bridgeIP, err)
			}
			if got != tc.want {
				t.Errorf("SubnetOf(%q) = %q, want %q", tc.bridgeIP, got, tc.want)
			}
		})
	}
}

// The NAT and forwarding rules are scoped to the subnet rather than to the
// bridge address, so a host address must never be used as the rule source.
func TestSubnetOfDropsHostBits(t *testing.T) {
	got, err := SubnetOf("192.168.1.1/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "192.168.1.1/24" {
		t.Error("SubnetOf kept the host bits; NAT rules would match only the gateway")
	}
}

// The host's own LAN must never be usable as the bridge subnet. 192.168.1.1/24
// was the original hardcoded value and is a very common home network, so this
// guards the case that would silently take the host offline.
func TestCheckSubnetConflictRejectsHostNetwork(t *testing.T) {
	addrs, err := hostIPv4Networks()
	if err != nil {
		t.Fatalf("enumerate host networks: %v", err)
	}
	if len(addrs) == 0 {
		t.Skip("no IPv4 interfaces on this host")
	}

	// Use a bridge name that cannot match a real interface: CheckSubnetConflict
	// intentionally skips the interface named after the bridge, so that a
	// bridge left over from an earlier run does not block startup. Passing a
	// live bridge name here would mask exactly the conflict we want to detect.
	const notAnInterface = "test-no-such-bridge"

	for _, cidr := range addrs {
		if err := CheckSubnetConflict(notAnInterface, cidr); err == nil {
			t.Errorf("CheckSubnetConflict(%q) = nil, want a conflict error", cidr)
		}
	}
}

// A bridge surviving from an earlier run must not prevent a restart: its own
// address is expected to be present and is not a conflict.
func TestCheckSubnetConflictIgnoresOwnBridge(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("enumerate interfaces: %v", err)
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			n, ok := addr.(*net.IPNet)
			if !ok || n.IP.To4() == nil || n.IP.IsLoopback() {
				continue
			}
			// Checking an interface's own address against itself is allowed.
			if err := CheckSubnetConflict(iface.Name, n.String()); err != nil {
				t.Errorf("CheckSubnetConflict(%q, %q) = %v, want nil (own bridge)",
					iface.Name, n.String(), err)
			}
			break
		}
	}
}

func TestCheckSubnetConflictAllowsUnusedSubnet(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3 (RFC 5737) and will not be configured
	// on a real host.
	if err := CheckSubnetConflict("br-fc", "203.0.113.1/24"); err != nil {
		t.Errorf("CheckSubnetConflict on an unused subnet returned: %v", err)
	}
}

func TestCheckSubnetConflictRejectsMalformedCIDR(t *testing.T) {
	for _, bad := range []string{"", "10.200.0.1", "garbage", "10.200.0.1/99"} {
		if err := CheckSubnetConflict("br-fc", bad); err == nil {
			t.Errorf("CheckSubnetConflict(%q) = nil, want an error", bad)
		}
	}
}

// hostIPv4Networks returns the CIDR of every IPv4 address on this host.
func hostIPv4Networks() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if n, ok := addr.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
				out = append(out, n.String())
			}
		}
	}
	return out, nil
}

func TestTapNames(t *testing.T) {
	got := TapNames("br0")
	want := []string{"br0-tap-0", "br0-tap-1"}

	if len(got) != len(want) {
		t.Fatalf("TapNames returned %d names, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TapNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Teardown derives tap names from the bridge name rather than receiving them,
// so both paths must agree or Cleanup silently leaves devices behind.
func TestTapNamesAreStable(t *testing.T) {
	first := TapNames("br0")
	second := TapNames("br0")

	for i := range first {
		if first[i] != second[i] {
			t.Errorf("TapNames is not deterministic: %q then %q", first[i], second[i])
		}
	}
}

func TestTapNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range TapNames("br0") {
		if seen[name] {
			t.Errorf("duplicate tap name %q — both VMs would share one device", name)
		}
		seen[name] = true
	}
}
