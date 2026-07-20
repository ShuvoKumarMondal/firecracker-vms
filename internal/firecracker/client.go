// Package firecracker is a client for the Firecracker VMM's REST API.
//
// The API is ordinary HTTP, served over a Unix domain socket rather than TCP.
// Every resource is configured before boot with a PUT; issuing the InstanceStart
// action then locks the configuration for the life of the VM.
package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Client talks to a single Firecracker process over its API socket.
type Client struct {
	http *http.Client
}

// NewClient returns a client bound to socketPath. The socket does not need to
// exist yet; each request dials it fresh.
func NewClient(socketPath string) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// do sends one API request. Firecracker answers a successful configuration call
// with 204 No Content, so any 2xx is success and the response body is only read
// to report failures.
func (c *Client) do(ctx context.Context, method, path string, payload any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}

	// The host is ignored because the transport always dials the socket, but
	// net/http still requires a syntactically valid URL.
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, body)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("%s %s: %s", method, path, faultMessage(resp))
}

// faultMessage returns Firecracker's own explanation of a failure, which is far
// more useful than the status line: "Cannot open the drive file" rather
// than "400 Bad Request".
func faultMessage(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return resp.Status
	}
	var fault struct {
		FaultMessage string `json:"fault_message"`
	}
	if json.Unmarshal(raw, &fault) == nil && fault.FaultMessage != "" {
		return fault.FaultMessage
	}
	return strings.TrimSpace(string(raw))
}

// MachineConfig is the guest's CPU and memory allocation.
type MachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
}

// BootSource is the guest kernel and its command line.
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
}

// Drive is a block device backed by a file on the host.
type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// NetworkInterface attaches a host tap device to the guest as a virtio-net NIC.
type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac,omitempty"`
}

// Logger directs the VMM's own log output to a file.
type Logger struct {
	LogPath string `json:"log_path"`
	Level   string `json:"level,omitempty"`
}

// Metrics directs the VMM's periodic metrics output to a file.
type Metrics struct {
	MetricsPath string `json:"metrics_path"`
}

// SetMachineConfig sets vCPU count and memory size.
func (c *Client) SetMachineConfig(ctx context.Context, cfg MachineConfig) error {
	return c.do(ctx, http.MethodPut, "/machine-config", cfg)
}

// SetBootSource sets the kernel image and boot arguments.
func (c *Client) SetBootSource(ctx context.Context, src BootSource) error {
	return c.do(ctx, http.MethodPut, "/boot-source", src)
}

// AddDrive attaches a block device.
func (c *Client) AddDrive(ctx context.Context, d Drive) error {
	return c.do(ctx, http.MethodPut, "/drives/"+d.DriveID, d)
}

// AddNetworkInterface attaches a NIC backed by a host tap device.
func (c *Client) AddNetworkInterface(ctx context.Context, n NetworkInterface) error {
	return c.do(ctx, http.MethodPut, "/network-interfaces/"+n.IfaceID, n)
}

// SetLogger configures VMM logging. It must be called before boot.
func (c *Client) SetLogger(ctx context.Context, l Logger) error {
	return c.do(ctx, http.MethodPut, "/logger", l)
}

// SetMetrics configures VMM metrics output. It must be called before boot.
func (c *Client) SetMetrics(ctx context.Context, m Metrics) error {
	return c.do(ctx, http.MethodPut, "/metrics", m)
}

// Action triggers an instance action such as InstanceStart or SendCtrlAltDel.
func (c *Client) Action(ctx context.Context, actionType string) error {
	return c.do(ctx, http.MethodPut, "/actions", struct {
		ActionType string `json:"action_type"`
	}{actionType})
}

// InstanceStart boots the guest and locks the configuration.
func (c *Client) InstanceStart(ctx context.Context) error {
	return c.Action(ctx, "InstanceStart")
}

// SendCtrlAltDel asks the guest to power off. On a guest running systemd this
// starts an orderly shutdown; a guest that ignores it must be killed.
func (c *Client) SendCtrlAltDel(ctx context.Context) error {
	return c.Action(ctx, "SendCtrlAltDel")
}
