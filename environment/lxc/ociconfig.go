// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// PVE creates a container from an OCI image's rootfs but does NOT apply the
// image's runtime config `Env` (PATH, JAVA_HOME, …) to the container's init. The
// Docker backend gets that for free; the LXC backend must fetch it from the
// registry and apply it itself, or the egg's startup command runs with a bare
// PATH and cannot find its own runtime (e.g. `java` at /opt/java/openjdk/bin).
//
// FetchImageEnv returns the image config `Env` slice ("KEY=value" entries) for an
// image reference, following the standard registry flow: token auth (discovered
// from a 401 challenge), manifest (resolving a multi-arch index to linux/amd64),
// then the config blob. It is best-effort: on any error the caller proceeds
// without image env rather than failing the install.
func FetchImageEnv(ctx context.Context, imageRef string) ([]string, error) {
	cfg, err := fetchImageConfig(ctx, imageRef)
	if err != nil {
		return nil, err
	}
	return cfg.Env, nil
}

type ociImageConfig struct {
	Env        []string `json:"Env"`
	Entrypoint []string `json:"Entrypoint"`
	Cmd        []string `json:"Cmd"`
	WorkingDir string   `json:"WorkingDir"`
	User       string   `json:"User"`
}

// ociManifestAccept lists the manifest media types we accept (multi-arch index
// and single-image, both OCI and Docker schema-2 forms).
var ociManifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

const (
	ociTargetOS    = "linux"
	ociTargetArch  = "amd64"
	ociHTTPTimeout = 30 * time.Second
)

// fetchImageConfig resolves an image reference to its runtime config.
func fetchImageConfig(ctx context.Context, imageRef string) (*ociImageConfig, error) {
	reg, repo, tag := parseImageRef(imageRef)
	if repo == "" {
		return nil, fmt.Errorf("oci: unparseable image reference %q", imageRef)
	}
	client := &http.Client{Timeout: ociHTTPTimeout}
	base := "https://" + reg

	// A token scoped to pull this repo, discovered from the registry's 401.
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, tag)
	token, err := fetchRegistryToken(ctx, client, manifestURL, repo)
	if err != nil {
		return nil, err
	}

	// Manifest — resolve a multi-arch index down to the linux/amd64 manifest.
	configDigest, err := resolveConfigDigest(ctx, client, base, repo, tag, token)
	if err != nil {
		return nil, err
	}

	// Config blob → the runtime config we care about.
	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", base, repo, configDigest)
	body, err := ociGet(ctx, client, blobURL, token, "application/vnd.oci.image.config.v1+json, application/json")
	if err != nil {
		return nil, fmt.Errorf("oci: fetch config blob: %w", err)
	}
	var wrap struct {
		Config ociImageConfig `json:"config"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("oci: decode config blob: %w", err)
	}
	return &wrap.Config, nil
}

// parseImageRef splits "registry/repo/name:tag" into its parts, defaulting the
// registry to docker.io (with the library/ prefix for bare names) and the tag to
// "latest" — matching Docker reference resolution.
func parseImageRef(ref string) (registry, repository, tag string) {
	ref = strings.TrimSpace(ref)
	tag = "latest"
	// Split off the tag (a ':' after the last '/'), leaving digests alone.
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		tag = ref[i+1:]
		ref = ref[:i]
	}
	slash := strings.IndexByte(ref, '/')
	if slash < 0 || (!strings.ContainsAny(ref[:slash], ".:") && ref[:slash] != "localhost") {
		// No registry component: Docker Hub.
		registry = "registry-1.docker.io"
		repository = ref
		if !strings.Contains(repository, "/") {
			repository = "library/" + repository
		}
		return registry, repository, tag
	}
	registry = ref[:slash]
	repository = ref[slash+1:]
	return registry, repository, tag
}

var bearerParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// fetchRegistryToken performs an unauthenticated probe to read the Bearer
// challenge, then exchanges it for a pull-scoped token. Registries that need no
// auth simply yield an empty token (the probe returns 200).
func fetchRegistryToken(ctx context.Context, client *http.Client, manifestURL, repo string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", ociManifestAccept)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oci: probe %s: %w", manifestURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "", nil // no auth required
	}
	challenge := resp.Header.Get("Www-Authenticate")
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer") {
		return "", fmt.Errorf("oci: unexpected auth challenge %q (status %d)", challenge, resp.StatusCode)
	}
	params := map[string]string{}
	for _, m := range bearerParamRe.FindAllStringSubmatch(challenge, -1) {
		params[strings.ToLower(m[1])] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("oci: auth challenge missing realm: %q", challenge)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	tokenURL := realm + "?service=" + params["service"] + "&scope=" + scope
	body, err := ociGet(ctx, client, tokenURL, "", "application/json")
	if err != nil {
		return "", fmt.Errorf("oci: fetch token: %w", err)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("oci: decode token: %w", err)
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	return tok.AccessToken, nil
}

// resolveConfigDigest fetches the manifest for a tag and returns the config blob
// digest, descending through a multi-arch index to the linux/amd64 manifest.
func resolveConfigDigest(ctx context.Context, client *http.Client, base, repo, ref, token string) (string, error) {
	body, err := ociGet(ctx, client, fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, ref), token, ociManifestAccept)
	if err != nil {
		return "", fmt.Errorf("oci: fetch manifest: %w", err)
	}
	var m struct {
		Config    struct{ Digest string } `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", fmt.Errorf("oci: decode manifest: %w", err)
	}
	if m.Config.Digest != "" {
		return m.Config.Digest, nil // already an image manifest
	}
	for _, sub := range m.Manifests {
		if sub.Platform.OS == ociTargetOS && sub.Platform.Architecture == ociTargetArch {
			return resolveConfigDigest(ctx, client, base, repo, sub.Digest, token)
		}
	}
	return "", fmt.Errorf("oci: no %s/%s manifest in index for %s", ociTargetOS, ociTargetArch, repo)
}

// ociGet performs a GET with optional bearer auth and returns the body.
func ociGet(ctx context.Context, client *http.Client, url, token, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return body, nil
}
