// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ociPullPoll is how often EnsureOCIImage polls the pull task for completion.
const ociPullPoll = 3 * time.Second

// EnsureOCIImage makes an egg's OCI image available on the node as a container
// template and returns its storage volid (e.g. "local:vztmpl/<name>.tar"). It is
// idempotent: an image already pulled to the storage is reused, so every server
// on the same egg shares a single pull.
//
// PVE ingests an OCI image by converting it to a vztmpl artifact (an async node
// task, POST .../oci-registry-pull). The artifact name is keyed on the image
// reference so the same image always maps to the same volid — that makes the
// reuse check exact and keeps re-pulls from piling up copies. PVE appends the
// ".tar" extension itself, so only the bare stem is sent as the filename.
func (c *PVEClient) EnsureOCIImage(ctx context.Context, node, storage, imageRef string) (string, error) {
	ref := strings.TrimSpace(imageRef)
	if ref == "" {
		return "", fmt.Errorf("oci image reference is required")
	}
	name := ociArtifactName(ref)
	if name == "" {
		return "", fmt.Errorf("oci image reference %q has no usable name", imageRef)
	}
	volid := fmt.Sprintf("%s:vztmpl/%s.tar", storage, name)

	present, err := c.storageHasVolID(ctx, node, storage, volid)
	if err != nil {
		return "", err
	}
	if present {
		return volid, nil
	}

	form := url.Values{"reference": {ref}, "filename": {name}}
	data, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/storage/%s/oci-registry-pull", node, storage), form)
	if err != nil {
		return "", fmt.Errorf("pull oci image %q: %w", ref, err)
	}
	upid, err := decodeTaskUPID(data)
	if err != nil {
		return "", err
	}
	if err := c.waitTask(ctx, node, upid); err != nil {
		return "", fmt.Errorf("pull oci image %q: %w", ref, err)
	}
	return volid, nil
}

// ociArtifactName derives a stable, filesystem-safe template stem from an OCI
// image reference: lowercased, with every run of non-alphanumeric characters
// collapsed to a single underscore and the ends trimmed. e.g.
// "ghcr.io/pelican-eggs/yolks:java_21" -> "ghcr_io_pelican_eggs_yolks_java_21".
func ociArtifactName(ref string) string {
	var b strings.Builder
	pendingUnderscore := false
	for _, r := range strings.ToLower(ref) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingUnderscore && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingUnderscore = false
			b.WriteRune(r)
			continue
		}
		pendingUnderscore = true
	}
	return b.String()
}

// storageHasVolID reports whether a vztmpl volid is already present on a storage.
func (c *PVEClient) storageHasVolID(ctx context.Context, node, storage, volid string) (bool, error) {
	path := fmt.Sprintf("/nodes/%s/storage/%s/content?content=vztmpl", node, storage)
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, fmt.Errorf("list storage %q content: %w", storage, err)
	}
	var wrap struct {
		Data []struct {
			VolID string `json:"volid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return false, fmt.Errorf("decode storage content: %w", err)
	}
	for _, it := range wrap.Data {
		if it.VolID == volid {
			return true, nil
		}
	}
	return false, nil
}

// decodeTaskUPID extracts a task UPID from an async PVE response ({"data":"UPID:..."}).
func decodeTaskUPID(data []byte) (string, error) {
	var wrap struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return "", fmt.Errorf("decode task upid: %w", err)
	}
	if wrap.Data == "" {
		return "", fmt.Errorf("empty task upid in response")
	}
	return wrap.Data, nil
}

// waitTask polls a node task until it stops, returning an error if it exits
// non-OK or the context is cancelled.
func (c *PVEClient) waitTask(ctx context.Context, node, upid string) error {
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", node, url.PathEscape(upid))
	ticker := time.NewTicker(ociPullPoll)
	defer ticker.Stop()
	for {
		data, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		var wrap struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &wrap); err != nil {
			return fmt.Errorf("decode task status: %w", err)
		}
		if wrap.Data.Status == "stopped" {
			// PVE reports success as "OK" and success-with-warnings as "WARNINGS: N";
			// anything else is a genuine failure.
			if es := wrap.Data.ExitStatus; es == "OK" || strings.HasPrefix(es, "WARNINGS:") {
				return nil
			}
			return fmt.Errorf("task %s failed: %s", upid, wrap.Data.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for task %s: %w", upid, ctx.Err())
		case <-ticker.C:
		}
	}
}
