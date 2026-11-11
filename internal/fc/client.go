// Package fc is a minimal client for the Firecracker REST API, which is
// served over a Unix domain socket.
package fc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client talks to one Firecracker process over its API socket.
type Client struct {
	http *http.Client
}

// NewClient returns a client bound to the given API socket path.
func NewClient(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		// One process, one socket: keep the connection warm between calls so
		// we time Firecracker, not socket setup.
		MaxIdleConns:    1,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

// APIError is returned for non-2xx responses.
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, body any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	// Host is ignored by the Unix dialer but must be present.
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: string(data)}
	}
	return nil
}

// ---- Request bodies (subset of Firecracker's swagger definition) ----

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
}

type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type MachineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	SMT        bool `json:"smt,omitempty"`
}

type Vsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type Metrics struct {
	MetricsPath string `json:"metrics_path"`
}

type SnapshotCreate struct {
	SnapshotType string `json:"snapshot_type,omitempty"` // "Full" | "Diff"
	SnapshotPath string `json:"snapshot_path"`
	MemFilePath  string `json:"mem_file_path"`
}

type MemBackend struct {
	BackendType string `json:"backend_type"` // "File" | "Uffd"
	BackendPath string `json:"backend_path"`
}

type SnapshotLoad struct {
	SnapshotPath    string     `json:"snapshot_path"`
	MemBackend      MemBackend `json:"mem_backend"`
	TrackDirtyPages bool       `json:"track_dirty_pages,omitempty"`
	ResumeVM        bool       `json:"resume_vm"`
}

// ---- Endpoints ----

func (c *Client) PutBootSource(ctx context.Context, b BootSource) error {
	return c.do(ctx, http.MethodPut, "/boot-source", b)
}

func (c *Client) PutDrive(ctx context.Context, d Drive) error {
	return c.do(ctx, http.MethodPut, "/drives/"+d.DriveID, d)
}

func (c *Client) PutMachineConfig(ctx context.Context, m MachineConfig) error {
	return c.do(ctx, http.MethodPut, "/machine-config", m)
}

func (c *Client) PutVsock(ctx context.Context, v Vsock) error {
	return c.do(ctx, http.MethodPut, "/vsock", v)
}

func (c *Client) PutMetrics(ctx context.Context, m Metrics) error {
	return c.do(ctx, http.MethodPut, "/metrics", m)
}

func (c *Client) action(ctx context.Context, t string) error {
	return c.do(ctx, http.MethodPut, "/actions", map[string]string{"action_type": t})
}

func (c *Client) StartInstance(ctx context.Context) error { return c.action(ctx, "InstanceStart") }
func (c *Client) FlushMetrics(ctx context.Context) error  { return c.action(ctx, "FlushMetrics") }

func (c *Client) setVMState(ctx context.Context, state string) error {
	return c.do(ctx, http.MethodPatch, "/vm", map[string]string{"state": state})
}

func (c *Client) Pause(ctx context.Context) error  { return c.setVMState(ctx, "Paused") }
func (c *Client) Resume(ctx context.Context) error { return c.setVMState(ctx, "Resumed") }

func (c *Client) CreateSnapshot(ctx context.Context, s SnapshotCreate) error {
	return c.do(ctx, http.MethodPut, "/snapshot/create", s)
}

func (c *Client) LoadSnapshot(ctx context.Context, s SnapshotLoad) error {
	return c.do(ctx, http.MethodPut, "/snapshot/load", s)
}
