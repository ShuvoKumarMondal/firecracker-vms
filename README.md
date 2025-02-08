### Step 1: Install Firecracker

If you haven't installed Firecracker yet, here’s how:

1. **Download Firecracker binary:**
Download Firecracker `v1.10.1-x86_64.tgz` from the official release:
    
    ```bash
    curl -LO https://github.com/firecracker-microvm/firecracker/releases/download/v1.10.1/firecracker-v1.10.1-x86_64.tgz
    
    ```
    
2. **Extract the tar file:**
    
    ```bash
    tar -xvzf firecracker-v1.10.1-x86_64.tgz
    
    ```
    
3. **Move Firecracker binary to the `$PATH`:**
    
    ```bash
    
    sudo mv firecracker-v1.10.1-x86_64/firecracker /usr/local/bin/
    
    ```
    
4. **Clean up:**
    
    ```bash
    rm -rf firecracker-v1.10.1-x86_64 firecracker-v1.10.1-x86_64.tgz
    
    ```
    

### Step 2: Download Root FileSyetem and Kernel

```bash
# Use Ubuntu 18.04 with ssh key
curl -o root-drive-with-ssh.img https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/disks/x86_64/ubuntu-18.04.ext4

curl -o vmlinux https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin

curl -o root-drive-ssh-key https://s3.amazonaws.com/spec.ccfc.min/ci-artifacts/disks/x86_64/ubuntu-18.04.id_rsa
```

### Step 3: Create a Bridge and add NAT Rule for the host’s Network interface

```bash
# Create a bridge
sudo ip link add name <bridgeName> type bridge

# Assign an IP address to the bridge
sudo ip addr add <ipAddress> dev <bridgeName>

# Delete the bridge (used in case of failure)
sudo ip link delete <bridgeName>

# Bring the bridge interface up
sudo ip link set dev <bridgeName> up

# Set up a NAT rule for the bridge
sudo iptables -t nat -A POSTROUTING -o <bridgeName> -j MASQUERADE

# Enable IP forwarding
sudo sysctl -w net.ipv4.ip_forward=1

# Add a NAT rule for the host's network interface (assuming 'wlo1' as the interface)
sudo iptables --table nat --append POSTROUTING --out-interface wlo1 -j MASQUERADE

```

### Step 4: Setup two tap devices

```bash
# Create TAP interface
sudo ip tuntap add dev mytap mode tap

# Bring up TAP interface
sudo ip link set dev mytap up

# Attach TAP to an existing bridge
sudo ip link set dev mytap master mybridge

```

### Step 5: Start Firecracker micro VM by using Firecracker SDK

For this step, we need to provide a configuration to start the Firecracker micro VM (mvm). The configuration file refers to the root file system, the kernel, the CPU, the Memory size, IP address, the tap device, mac address it will use.

### Step 6: SSH into the firecracker micro VMs, Ping One mvm from another, Ping 8.8.8.8 from each mvm.

![Screenshot from 2025-02-07 01-51-09.png](attachment:4ba5bcf0-e587-4d91-bd77-28dfc9453590:Screenshot_from_2025-02-07_01-51-09.png)

### Step 7: Install htop in each firecracker micro vm

![Screenshot from 2025-02-07 03-58-42.png](attachment:9a099064-f8d3-4e33-8dc6-2fe72ba819df:Screenshot_from_2025-02-07_03-58-42.png)
