// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Container power states as reported by the PVE API.
const (
	StatusRunning = "running"
	StatusStopped = "stopped"
)

const (
	apiBasePath       = "/api2/json"
	defaultPVETimeout = 30 * time.Second
	// waitPollInterval is how often WaitForStatus re-checks the container state.
	waitPollInterval = 2 * time.Second
)

// ErrPVEClient wraps PVEClient construction/validation failures.
var ErrPVEClient = errors.New("invalid pve client config")

// PVEClient is a minimal Proxmox VE REST client for the container power
// operations Wings drives directly (rather than through OpenTofu): start,
// graceful shutdown, force stop, and status. It authenticates with a PVE API
// token and never logs the token.
type PVEClient struct {
	endpoint string
	token    string
	http     *http.Client
}

// PVEClientConfig configures a PVEClient. The API token has the form
// user@realm!tokenid=secret and is sent in the Authorization header only.
type PVEClientConfig struct {
	Endpoint string
	APIToken string
	Insecure bool
	Timeout  time.Duration
}

// NewPVEClient validates config and returns a ready client.
func NewPVEClient(cfg PVEClientConfig) (*PVEClient, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("%w: endpoint is required", ErrPVEClient)
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("%w: endpoint %q must be an http(s) URL", ErrPVEClient, endpoint)
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, fmt.Errorf("%w: api token is required", ErrPVEClient)
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultPVETimeout
	}
	transport := &http.Transport{}
	if cfg.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &PVEClient{
		endpoint: endpoint,
		token:    cfg.APIToken,
		http:     &http.Client{Timeout: timeout, Transport: transport},
	}, nil
}

// ContainerStatus is the subset of the PVE status/current response Wings needs.
type ContainerStatus struct {
	Status string `json:"status"`
	// Uptime is the container uptime in seconds (0 when stopped).
	Uptime int64  `json:"uptime"`
	Name   string `json:"name"`
	VMID   int    `json:"vmid"`
}

// Running reports whether the container is in the running state.
func (s ContainerStatus) Running() bool { return s.Status == StatusRunning }

// Status returns the current power state of the container.
func (c *PVEClient) Status(ctx context.Context, node string, vmid int) (ContainerStatus, error) {
	data, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/lxc/%d/status/current", node, vmid), nil)
	if err != nil {
		return ContainerStatus{}, err
	}
	var wrap struct {
		Data ContainerStatus `json:"data"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return ContainerStatus{}, fmt.Errorf("decode container status: %w", err)
	}
	return wrap.Data, nil
}

// Start powers on the container. The PVE call is asynchronous; use WaitForStatus
// to block until the container reaches the running state.
func (c *PVEClient) Start(ctx context.Context, node string, vmid int) error {
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/status/start", node, vmid), url.Values{})
	return err
}

// Shutdown requests a graceful shutdown, allowing timeout seconds before the
// PVE side force-stops. A zero timeout uses the node default.
func (c *PVEClient) Shutdown(ctx context.Context, node string, vmid int, timeout time.Duration) error {
	form := url.Values{}
	if timeout > 0 {
		form.Set("timeout", fmt.Sprintf("%d", int(timeout.Seconds())))
	}
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/status/shutdown", node, vmid), form)
	return err
}

// Stop force-stops the container immediately.
func (c *PVEClient) Stop(ctx context.Context, node string, vmid int) error {
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/lxc/%d/status/stop", node, vmid), url.Values{})
	return err
}

// WaitForStatus polls until the container reports target, the context is
// cancelled, or the deadline passes.
func (c *PVEClient) WaitForStatus(ctx context.Context, node string, vmid int, target string) error {
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()
	for {
		st, err := c.Status(ctx, node, vmid)
		if err != nil {
			return err
		}
		if st.Status == target {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for container %d to be %q: %w", vmid, target, ctx.Err())
		case <-ticker.C:
		}
	}
}

// do performs an authenticated PVE API request and returns the raw body. A form
// (may be empty, non-nil) makes it a form-encoded POST body.
func (c *PVEClient) do(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+apiBasePath+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response for %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pve api %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
