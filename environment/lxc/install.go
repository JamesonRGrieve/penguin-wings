// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	if err := e.power.SetEntrypoint(ctx, e.node, e.vmid, keepaliveEntrypoint); err != nil {
		return fmt.Errorf("set keepalive entrypoint: %w", err)
	}
	if err := e.power.Start(ctx, e.node, e.vmid); err != nil {
		return fmt.Errorf("start for install: %w", err)
	}
	// 2. Push and run the egg's own install script as root.
	if err := e.ssh.Push(e.vmid, []byte(spec.Script), "0755", eggScriptPath); err != nil {
		return fmt.Errorf("push egg install script: %w", err)
	}
	if err := e.ssh.Push(e.vmid, []byte(installWrapper(spec.Env, e.spec.Nameservers)), "0755", wrapperPath); err != nil {
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
	if err := e.ssh.Push(e.vmid, []byte(runScript(spec.Invocation, spec.Env)), "0755", runScriptPath); err != nil {
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
func installWrapper(env map[string]string, nameservers []string) string {
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
func runScript(invocation string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\ncd " + dataDir + "\n")
	writeExports(&b, env)
	b.WriteString(invocation + "\n")
	return b.String()
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
