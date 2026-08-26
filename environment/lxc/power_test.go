// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "penguin@pve!wings=00000000-0000-0000-0000-000000000000"

func newTestClient(t *testing.T, h http.HandlerFunc) *PVEClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewPVEClient(PVEClientConfig{Endpoint: srv.URL, APIToken: testToken})
	if err != nil {
		t.Fatalf("NewPVEClient: %v", err)
	}
	return c
}

func TestNewPVEClientValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  PVEClientConfig
	}{
		{"empty endpoint", PVEClientConfig{APIToken: testToken}},
		{"non-http endpoint", PVEClientConfig{Endpoint: "node:8006", APIToken: testToken}},
		{"empty token", PVEClientConfig{Endpoint: "https://node:8006"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewPVEClient(tc.cfg); !errors.Is(err, ErrPVEClient) {
				t.Errorf("want ErrPVEClient, got %v", err)
			}
		})
	}
	if _, err := NewPVEClient(PVEClientConfig{Endpoint: "https://node:8006/", APIToken: testToken}); err != nil {
		t.Errorf("valid config: unexpected error %v", err)
	}
}

func TestPVEClientStatus(t *testing.T) {
	t.Parallel()
	var gotAuth, gotPath, gotMethod string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		fmt.Fprint(w, `{"data":{"status":"running","uptime":123,"name":"mc-01","vmid":100}}`)
	})
	st, err := c.Status(context.Background(), "lab-primus", 100)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Running() || st.Uptime != 123 || st.Name != "mc-01" || st.VMID != 100 {
		t.Errorf("status = %+v, want running/123/mc-01/100", st)
	}
	if gotAuth != "PVEAPIToken="+testToken {
		t.Errorf("auth header = %q, want PVEAPIToken=<token>", gotAuth)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api2/json/nodes/lab-primus/lxc/100/status/current" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestPVEClientPowerRequests(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		call        func(c *PVEClient) error
		wantPath    string
		wantTimeout string
	}{
		{"start", func(c *PVEClient) error { return c.Start(context.Background(), "n1", 5) }, "/api2/json/nodes/n1/lxc/5/status/start", ""},
		{"stop", func(c *PVEClient) error { return c.Stop(context.Background(), "n1", 5) }, "/api2/json/nodes/n1/lxc/5/status/stop", ""},
		{"shutdown", func(c *PVEClient) error { return c.Shutdown(context.Background(), "n1", 5, 30*time.Second) }, "/api2/json/nodes/n1/lxc/5/status/shutdown", "30"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotPath, gotMethod, gotTimeout string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				gotTimeout = r.PostFormValue("timeout")
				fmt.Fprint(w, `{"data":"UPID:task"}`)
			})
			if err := tc.call(c); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotTimeout != tc.wantTimeout {
				t.Errorf("timeout form = %q, want %q", gotTimeout, tc.wantTimeout)
			}
		})
	}
}

func TestPVEClientErrorResponse(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Permission check failed")
	})
	_, err := c.Status(context.Background(), "n1", 1)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want error mentioning 403, got %v", err)
	}
}

func TestWaitForStatus(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		status := StatusStopped
		if calls.Add(1) >= 2 {
			status = StatusRunning
		}
		fmt.Fprintf(w, `{"data":{"status":%q,"vmid":1}}`, status)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.WaitForStatus(ctx, "n1", 1, StatusRunning); err != nil {
		t.Fatalf("WaitForStatus: %v", err)
	}
	if calls.Load() < 2 {
		t.Errorf("expected at least 2 polls, got %d", calls.Load())
	}
}

// TestPVEClientStatusIntegration reads a real container's status (GET only, no
// mutation). Skipped unless the target env is set.
func TestPVEClientStatusIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_PVE_ENDPOINT")
	token := os.Getenv("TEST_PVE_API_TOKEN")
	node := os.Getenv("TEST_PVE_NODE")
	vmidStr := os.Getenv("TEST_PVE_VMID")
	if endpoint == "" || token == "" || node == "" || vmidStr == "" {
		t.Skip("set TEST_PVE_ENDPOINT/_API_TOKEN/_NODE/_VMID to run")
	}
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil {
		t.Fatalf("bad TEST_PVE_VMID %q: %v", vmidStr, err)
	}
	c, err := NewPVEClient(PVEClientConfig{Endpoint: endpoint, APIToken: token, Insecure: os.Getenv("TEST_PVE_INSECURE") == "true"})
	if err != nil {
		t.Fatalf("NewPVEClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := c.Status(ctx, node, vmid)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Status != StatusRunning && st.Status != StatusStopped {
		t.Errorf("unexpected status %q", st.Status)
	}
}
