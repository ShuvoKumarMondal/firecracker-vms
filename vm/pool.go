package vm

import (
	"fmt"
	"net"
)

// BuildConfigs assembles one VMConfig per tap device, allocating a unique IP
// and MAC to each from the bridge's subnet. The gateway (the bridge address
// itself) is reserved; guests are numbered from the following address.
func BuildConfigs(bridgeIP string, taps []string) ([]VMConfig, error) {
	ips, err := allocateIPs(bridgeIP, len(taps))
	if err != nil {
		return nil, err
	}

	configs := make([]VMConfig, len(taps))
	for i, tap := range taps {
		configs[i] = VMConfig{
			ID:         fmt.Sprintf("vm%d", i+1),
			SocketPath: fmt.Sprintf("/tmp/firecracker-vm%d.sock", i+1),
			TapName:    tap,
			MacAddress: macForIndex(i + 1),
			IPAddress:  ips[i],
			BridgeIP:   bridgeIP,
		}
	}
	return configs, nil
}

// allocateIPs returns count host addresses from the bridge subnet, starting one
// past the gateway. It fails rather than wrapping past the broadcast address, so
// asking for more VMs than the subnet holds is a clear error instead of a
// collision.
func allocateIPs(bridgeCIDR string, count int) ([]string, error) {
	if count <= 0 {
		return nil, fmt.Errorf("vm count must be positive, got %d", count)
	}

	gateway, subnet, err := net.ParseCIDR(bridgeCIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid bridge CIDR %q: %v", bridgeCIDR, err)
	}
	broadcast := broadcastAddr(subnet)

	ip := gateway.To4()
	if ip == nil {
		return nil, fmt.Errorf("bridge address %q is not IPv4", bridgeCIDR)
	}

	ips := make([]string, 0, count)
	for len(ips) < count {
		ip = nextIP(ip)
		if !subnet.Contains(ip) || ip.Equal(broadcast) {
			return nil, fmt.Errorf(
				"subnet %s has room for %d VMs, not %d — use a larger subnet or a smaller -n",
				subnet, len(ips), count)
		}
		ips = append(ips, ip.String())
	}
	return ips, nil
}

// nextIP returns the address one higher than ip, operating on its 4-byte form.
func nextIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

// broadcastAddr returns the all-ones host address of the subnet, which is not
// assignable to a guest.
func broadcastAddr(subnet *net.IPNet) net.IP {
	ip := subnet.IP.To4()
	if ip == nil {
		return nil
	}
	b := make(net.IP, len(ip))
	for i := range ip {
		b[i] = ip[i] | ^subnet.Mask[i]
	}
	return b
}

// macForIndex returns a locally-administered unicast MAC for VM index i. The
// AA:BB:CC prefix is fixed and the index occupies the last two octets, so the
// address is unique for any subnet size and stable across runs.
func macForIndex(i int) string {
	return fmt.Sprintf("AA:BB:CC:00:%02X:%02X", (i>>8)&0xFF, i&0xFF)
}
