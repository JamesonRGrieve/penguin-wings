// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/apex/log"
)

const (
	// containerUser is the Pelican-convention non-root user egg images run as; the
	// installed game files are chowned to it.
	containerUser = "container"
	// serverDir is where egg install scripts write files; it is symlinked to the
	// data dir so unmodified egg scripts (which hardcode /mnt/server) just work.
	serverDir = "/mnt/server"
	// dataDir is the container's persistent working directory (yolk convention).
	dataDir = "/home/container"
	// keepaliveEntrypoint keeps the CT running during install so pct exec/push work
	// (the game images have no long-lived init of their own until the run-script).
	keepaliveEntrypoint = "/bin/sleep 2147483647"
	// runScriptPath is the entrypoint script that launches the game.
	runScriptPath = "/home/container/.penguin-run.sh"
	// eggScriptPath / wrapperPath are transient in-CT paths for the egg install.
	eggScriptPath = "/tmp/penguin-egg-install"
	wrapperPath   = "/tmp/penguin-install"
)

// InstallSpec is everything the LXC install phase needs, all sourced from the egg
// via the Panel — never edited: the egg's install script, its variables, and the
// resolved startup command that becomes the run-script.
type InstallSpec struct {
	Script     string            // the egg's installation script, verbatim
	Env        map[string]string // egg variables, exported for install + run
	Invocation string            // Panel-resolved startup command (run-script body)
}

// Install realizes a server end to end without touching the egg: create the CT
// from the egg's OCI image, run the egg's own install script inside it as
// container-root over the scoped SSH channel, then wire the run-script as the
// entrypoint so a later Start boots straight into the game. Ends stopped.
func (e *Environment) Install(ctx context.Context, spec InstallSpec) error {
	if e.ssh == nil {
		return fmt.Errorf("lxc install: ssh channel not configured")
	}
	// 1. Create the CT from the egg image (idempotent) and boot a keepalive so the
	//    scoped exec/push channel can operate.
	if err := e.Create(); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	// PVE does not apply the egg OCI image's config env (PATH, JAVA_HOME, …) to
	// the container init, so fetch it from the registry and export it in the
	// install + run scripts — otherwise the egg's install tools and its game
	// runtime aren't on PATH. Best-effort: a fetch failure just means a bare PATH.
	imageEnv, err := FetchImageEnv(ctx, e.spec.Image)
	if err != nil {
		log.WithField("image", e.spec.Image).WithField("error", err).
			Warn("lxc: could not fetch OCI image env; container will run with a bare PATH")
		imageEnv = nil
	}
	if err := e.power.SetEntrypoint(ctx, e.node, e.vmid, keepaliveEntrypoint); err != nil {
		return fmt.Errorf("set keepalive entrypoint: %w", err)
	}
	if err := e.power.Start(ctx, e.node, e.vmid); err != nil {
		return fmt.Errorf("start for install: %w", err)
	}
	// 2. Push and run the egg's own install script as root. Some eggs store the
	//    script with CRLF line endings; strip the carriage returns so POSIX bash
	//    doesn't choke on `$'\r'` (a run-time normalization, not an egg edit).
	eggScript := strings.ReplaceAll(spec.Script, "\r\n", "\n")
	if err := e.ssh.Push(e.vmid, []byte(eggScript), "0755", eggScriptPath); err != nil {
		return fmt.Errorf("push egg install script: %w", err)
	}
	if err := e.ssh.Push(e.vmid, []byte(installWrapper(spec.Env, e.spec.Nameservers, imageEnv)), "0755", wrapperPath); err != nil {
		return fmt.Errorf("push install wrapper: %w", err)
	}
	if out, err := e.ssh.PctExec(e.vmid, "bash", wrapperPath); err != nil {
		return fmt.Errorf("run egg install: %w\n%s", err, out)
	}
	// 3. Hand ownership of the installed files to the non-root game user.
	if out, err := e.ssh.PctExec(e.vmid, "chown", "-R", containerUser+":"+containerUser, dataDir); err != nil {
		return fmt.Errorf("chown data dir: %w\n%s", err, out)
	}
	// 4. Install the run-script, chown it, and point the entrypoint at it; stop so
	//    the next Start boots straight into the game.
	if err := e.ssh.Push(e.vmid, []byte(runScript(spec.Invocation, spec.Env, imageEnv)), "0755", runScriptPath); err != nil {
		return fmt.Errorf("push run-script: %w", err)
	}
	if out, err := e.ssh.PctExec(e.vmid, "chown", containerUser+":"+containerUser, runScriptPath); err != nil {
		return fmt.Errorf("chown run-script: %w\n%s", err, out)
	}
	if err := e.power.Stop(ctx, e.node, e.vmid); err != nil {
		return fmt.Errorf("stop after install: %w", err)
	}
	if err := e.power.SetEntrypoint(ctx, e.node, e.vmid, runScriptPath); err != nil {
		return fmt.Errorf("set run entrypoint: %w", err)
	}
	return nil
}

// installWrapper builds the script the install runs inside the CT: it exposes the
// egg's /mnt/server convention (symlinked to the data dir), exports the egg
// variables, and invokes the egg's own script. Values are single-quote escaped.
func installWrapper(env map[string]string, nameservers []string, imageEnv []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")
	// OCI (unmanaged) app containers get no resolver from PVE, so write one here:
	// the egg install scripts fetch game files over the network.
	if len(nameservers) > 0 {
		b.WriteString(": > /etc/resolv.conf\n")
		for _, ns := range nameservers {
			b.WriteString("echo 'nameserver " + ns + "' >> /etc/resolv.conf\n")
		}
	}
	// The egg's install script relies on the image's own env (its tools live on
	// the image PATH, not the bare container default) — export it first.
	writeImageEnvExports(&b, imageEnv)
	b.WriteString("ln -sfn " + dataDir + " " + serverDir + "\n")
	writeExports(&b, env)
	b.WriteString("cd " + serverDir + "\n")
	b.WriteString("bash " + eggScriptPath + "\n")
	return b.String()
}

// runScript is the entrypoint that launches the game as the container user in the
// data dir. It exports the egg variables (a resolved invocation may still
// reference them as $VARS at runtime) then runs the invocation directly — not via
// exec — so compound startups (subshells, backgrounded readers) behave, matching
// the upstream yolk entrypoint.
func runScript(invocation string, env map[string]string, imageEnv []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\ncd " + dataDir + "\n")
	// Apply the image env first (PATH to the game runtime), then the egg vars.
	writeImageEnvExports(&b, imageEnv)
	writeExports(&b, env)
	b.WriteString(invocation + "\n")
	return b.String()
}

// writeImageEnvExports emits `export K=V` lines for an OCI image config's Env
// ("K=value" entries), single-quote-escaping the value. PVE does not apply these
// to the container init, so the install wrapper and run-script do — putting the
// image's PATH (and JAVA_HOME etc.) in scope for the egg's tools and runtime.
func writeImageEnvExports(b *strings.Builder, imageEnv []string) {
	for _, kv := range imageEnv {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		b.WriteString("export " + kv[:i] + "=" + singleQuote(kv[i+1:]) + "\n")
	}
}

// writeExports emits sorted, single-quote-escaped `export K=V` lines.
func writeExports(b *strings.Builder, env map[string]string) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("export " + k + "=" + singleQuote(env[k]) + "\n")
	}
}

// singleQuote wraps s in a POSIX single-quoted string, escaping embedded quotes.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
