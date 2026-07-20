# firecracker-vms

Boots a fleet of [Firecracker](https://firecracker-microvm.github.io/) microVMs on a Linux host and wires them into a bridged network — in Go, with one command.

Firecracker is the VMM behind AWS Lambda and Fargate: a minimal hypervisor that boots a Linux guest in ~125 ms with no BIOS, no PCI and no device emulation beyond virtio. This project is the layer above it — the part that has to manage processes, host network state, and cleanup.

```bash
make setup && make run
```

---

## Features

**MicroVM lifecycle**
- Boots any number of Firecracker microVMs (2 vCPU / 512 MiB each) from a single command — `make run N=5` for five
- Talks to Firecracker through a **hand-written API client** over its Unix-socket REST API — no external dependencies
- Each guest gets its own writable root disk, copied on demand from a cached base image, so concurrent writes can't corrupt a shared image
- Graceful shutdown: guests are asked to power off, then the VMM is forced down if they don't
- Handles `SIGINT`/`SIGTERM`, and detects guests that exit on their own

**Networking**
- Automatic bridge + one TAP device per VM, each with a unique MAC and IP
- **IP and MAC allocation** from the bridge subnet — each VM takes the next free address, and asking for more VMs than the subnet holds is a clear error, not a collision
- VM-to-VM connectivity, and outbound internet via NAT
- **Auto-detects the host uplink** from the default route — no hardcoded interface name
- **Subnet-conflict preflight** refuses to start if the guest subnet overlaps a network already on the host
- Installs the `FORWARD` rules that Docker's `DROP` policy otherwise silently blocks

**Operational safety**
- **Idempotent** — every step checks for what it's about to create, so re-runs neither fail nor duplicate iptables rules
- **Full teardown on exit** — bridge, TAPs and firewall rules are all removed
- `make down` recovers the host after a crash or `SIGKILL`
- Checksum-verified image downloads
- Per-VM VMM log and guest serial console written to files, not your terminal

## Architecture

```
                    ┌──────────────────────────── Linux host ─────────────────────────────┐
                    │                                                                     │
   ./bin/fcvms ─────┼──▶ creates bridge br-fc (10.200.0.1/24)                             │
                    │    installs NAT + FORWARD rules on the default-route interface      │
                    │                                                                     │
                    │      ┌──────────────┐                    ┌──────────────┐           │
                    │      │  firecracker │                    │  firecracker │           │
                    │      │     (vm1)    │                    │     (vm2)    │           │
                    │      └──────┬───────┘                    └──────┬───────┘           │
                    │             │ virtio-net                        │                   │
                    │      ┌──────▼───────┐                    ┌──────▼───────┐           │
                    │      │ br-fc-tap-0  │                    │ br-fc-tap-1  │           │
                    │      └──────┬───────┘                    └──────┬───────┘           │
                    │             │                                   │                   │
                    │      ┌──────▼───────────────────────────────────▼───────┐           │
                    │      │            br-fc  (virtual L2 switch)            │           │
                    │      └──────────────────────┬───────────────────────────┘           │
                    │                             │ MASQUERADE                            │
                    │                    ┌────────▼────────┐                              │
                    │                    │ default route   │──────────────▶ internet      │
                    │                    │ (wlo1 / eth0…)  │                              │
                    │                    └─────────────────┘                              │
                    └─────────────────────────────────────────────────────────────────────┘
```

The default `-n 2` produces the layout below; each extra VM takes the next tap, IP and MAC:

| VM | IP | MAC | TAP | Resources |
|---|---|---|---|---|
| vm1 | `10.200.0.2` | `AA:BB:CC:00:00:01` | `br-fc-tap-0` | 2 vCPU / 512 MiB |
| vm2 | `10.200.0.3` | `AA:BB:CC:00:00:02` | `br-fc-tap-1` | 2 vCPU / 512 MiB |
| … | `10.200.0.N+1` | `AA:BB:CC:00:00:0N` | `br-fc-tap-N` | 2 vCPU / 512 MiB |

Guest kernel `5.10.225`, rootfs Ubuntu 18.04, Firecracker `v1.7.0`.

---

## Requirements

| | |
|---|---|
| OS | Linux with KVM — `/dev/kvm` must exist and be readable |
| CPU | Intel VT-x or AMD-V |
| Go | 1.23 or newer |
| Privileges | `sudo`, for `ip` and `iptables` |
| Disk | ~1.2 GB for the kernel and per-VM root disks |

If `/dev/kvm` is not readable:

```bash
sudo usermod -aG kvm $USER   # then log out and back in
```

## Installation

```bash
git clone https://github.com/ShuvoKumarMondal/firecracker-vms.git
cd firecracker-vms
make setup
```

`make setup` downloads the guest kernel, the root filesystem and the SSH key into `resources/`, verifies their checksums, and installs the `firecracker` binary to `/usr/local/bin` (this step asks for sudo). It is safe to re-run — everything is cached.

## Usage

### Boot the microVMs

```bash
make run          # two VMs (the default)
make run N=5      # five VMs
```

`make run` builds the launcher and runs `./bin/fcvms -n <N>`.

```
Bridge br-fc is up with address 10.200.0.1/24, NAT via wlo1
Tap br-fc-tap-0 attached to bridge br-fc
Tap br-fc-tap-1 attached to bridge br-fc
microVM vm1 running: ip=10.200.0.2 tap=br-fc-tap-0 console=/tmp/firecracker-vm1.sock.console
microVM vm2 running: ip=10.200.0.3 tap=br-fc-tap-1 console=/tmp/firecracker-vm2.sock.console
2 microVMs running — press Ctrl+C to shut down
```

Ctrl+C shuts every guest down and removes all the host state it created.

### Connect to a guest

```bash
chmod 600 resources/ubuntu-18.04.id_rsa
ssh -i resources/ubuntu-18.04.id_rsa root@10.200.0.2
```

### Verify it works

From inside a guest:

```bash
uname -a                 # Linux ... 5.10.225 ... x86_64
nproc                    # 2
free -m                  # ~512 MiB
ip addr show eth0        # 10.200.0.2/24

ping -c3 10.200.0.3      # the other microVM
ping -c3 8.8.8.8         # the internet, through NAT
```

From the host, while running:

```bash
ip -br link show br-fc                     # bridge is up
bridge link show                           # TAPs enslaved
tail -f /tmp/firecracker-vm1.sock.console  # vm1's serial console
```

After Ctrl+C, the host should be clean:

```bash
ip link show br-fc                          # "does not exist"
sudo iptables -t nat -S POSTROUTING         # no leftover MASQUERADE rules
```

### Installing packages in a guest

The bundled rootfs is Ubuntu 18.04, which is past end-of-life, so its archives have moved:

```bash
sed -i 's|archive.ubuntu.com|old-releases.ubuntu.com|g;
        s|security.ubuntu.com|old-releases.ubuntu.com|g' /etc/apt/sources.list
echo 'nameserver 8.8.8.8' > /etc/resolv.conf
apt-get update && apt-get install -y htop
```

### Make targets

| Target | Purpose |
|---|---|
| `make setup` | Fetch kernel/rootfs (checksum-verified) and install Firecracker |
| `make run` | Build and boot N microVMs (default 2; `make run N=5`) |
| `make down` | Remove leftover bridge, TAPs and firewall rules |
| `make build` | Compile to `bin/fcvms` |
| `make vet` / `make fmt` | Static analysis / formatting |
| `make clean` | `make down` plus build output and per-VM disks |
| `make help` | List all targets |

## Configuration

The VM count is a flag; everything else is a constant, chosen to be easy to find.

| What | Where |
|---|---|
| Number of VMs | `-n` flag (`make run N=5`) |
| Bridge name and subnet | `bridgeName`, `bridgeIP` in `main.go` |
| Guest IPs and MACs | allocated from the subnet in `vm/pool.go` |
| Shutdown grace period | `shutdownGrace` in `main.go` |
| vCPU count and memory | `vcpuCount`, `memSizeMiB` in `vm/firecracker.go` |
| Kernel command line | `bootArgs` in `vm/firecracker.go` |
| Image directory | `ResourcesDir` in `vm/firecracker.go` |

Two scripts read environment variables:

```bash
FIRECRACKER_VERSION=v1.7.0 make setup      # pin a different Firecracker release
BRIDGE=br-fc SUBNET=10.200.0.0/24 make down
```

If you change `bridgeIP`, change the guest IPs to match — they must be in the same subnet.

## How it works

**Firecracker's API is HTTP over a Unix socket.** Each VM is a `firecracker` process listening on `/tmp/firecracker-vmN.sock`. Kernel, root drive, vCPU/memory and network interface are `PUT` to that socket *before* boot; once `InstanceStart` is issued the configuration is locked. This project drives that with a small hand-written client (`internal/firecracker`) — an `http.Transport` whose `DialContext` dials the socket — rather than a third-party SDK, so the repo has no external Go dependencies.

**Networking is plain Linux primitives.** A bridge is an L2 switch. A TAP device is a virtual NIC whose userspace end belongs to Firecracker: the guest writes a frame to virtio-net, Firecracker writes it to the TAP, the bridge forwards it. Internet access then needs three things:

- `net.ipv4.ip_forward=1`, so the host routes between interfaces
- a `MASQUERADE` rule on the **uplink** — not the bridge, a common mistake that NATs host→guest traffic instead
- explicit `FORWARD` rules, because Docker sets the `FORWARD` policy to `DROP`; without them guests have working NAT and still reach nothing

**Guest addressing rides in on the kernel command line.** The launcher builds `ip=10.200.0.2::10.200.0.1:255.255.255.0::eth0:off` into the boot arguments (`bootArgs` in `vm/firecracker.go`), so the guest is configured at boot with no DHCP server involved.

**Cleanup is not optional.** Bridges, TAPs and iptables rules are global host state that outlives the process. The launcher removes everything it created on exit, and `hack/teardown.sh` recovers the host when it can't.

## Project layout

```
main.go                     entry point: flags, bring up network, boot VMs, wait, tear down
internal/firecracker/
  client.go                   typed REST calls over the Unix socket
  machine.go                  spawn, boot, wait on, and shut down a firecracker process
vm/network.go               bridge, TAP, NAT/forwarding, conflict preflight, teardown
vm/pool.go                  IP and MAC allocation, per-VM config assembly
vm/firecracker.go           per-VM resource prep and launch
hack/fetch-resources.sh     downloads kernel/rootfs, installs firecracker
hack/teardown.sh            recovers the host after a crash or SIGKILL
```

Per-VM runtime files: `/tmp/firecracker-vmN.sock` (API socket), `.log` (VMM log), `.console` (guest serial console).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `bridge subnet ... overlaps ...` | The guest subnet collides with a host network. Change `bridgeIP` in `main.go`. This guard exists because `192.168.1.1/24` — a common default — is usually the *router's own address*, and assigning it to a local bridge silently breaks host connectivity. |
| `sudo needs a password here` | Privileged commands capture their own output, so a password prompt has nowhere to render. Run `sudo -v` first. |
| `missing ... run 'make setup'` | Kernel or rootfs absent from `resources/`. |
| Guests can't reach the internet | The NAT rule binds to whatever `ip route get 8.8.8.8` reported at startup. If a VPN comes up or drops mid-run, that interface is no longer the uplink — `make down`, then restart. |
| Anything left behind after a crash | `make down`. |
| SSH rejects the key | `chmod 600 resources/ubuntu-18.04.id_rsa`. |

## Limitations

Honest scope, since this is a learning project:

- Single host — no scheduler, no multi-node placement
- Every VM is the same size (2 vCPU / 512 MiB); no per-VM sizing
- No REST API; it is a flag-driven launcher, not a long-running service
- No [jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md), so guests are not chroot/cgroup/seccomp isolated
- No snapshot/restore
- Privileged network operations shell out to `ip`/`iptables` via `sudo` rather than using netlink directly

## Roadmap

1. **REST API** — VM lifecycle over HTTP, turning the launcher into a control plane
2. **OCI images** — build a bootable rootfs from a container image (`nginx:alpine`)
3. **Jailer** — chroot, cgroups v2 and seccomp per guest
4. **Snapshot/restore** — pause to disk and restore in milliseconds, the mechanism behind Lambda's cold starts
