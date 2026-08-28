// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"fmt"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const sshDialTimeout = 20 * time.Second

// SSHConfig configures the scoped node channel used ONLY for the install phase.
type SSHConfig struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	Wrapper        string // sudo-restricted pct wrapper (e.g. /usr/local/bin/penguin-pct)
	StagingDir     string // account-owned dir push sources must live in
}

// SSHClient runs the scoped pct exec/push operations on the PVE node. It NEVER
// runs arbitrary node commands — only `sudo <wrapper> exec|push` — so even a
// compromised Wings can reach only Penguin's own containers as container-root,
// never the node itself. Each call opens a fresh connection.
type SSHClient struct {
	cfg    SSHConfig
	client *ssh.ClientConfig
	addr   string
}

// NewSSHClient builds an SSH client from the scoped account's key.
func NewSSHClient(cfg SSHConfig) (*SSHClient, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("lxc ssh: host is required")
	}
	if strings.TrimSpace(cfg.PrivateKeyPath) == "" {
		return nil, fmt.Errorf("lxc ssh: private_key_path is required")
	}
	raw, err := os.ReadFile(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("lxc ssh: read key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("lxc ssh: parse key: %w", err)
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Wrapper == "" {
		cfg.Wrapper = "/usr/local/bin/penguin-pct"
	}
	if cfg.StagingDir == "" {
		cfg.StagingDir = "/var/lib/penguin"
	}
	return &SSHClient{
		cfg:  cfg,
		addr: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		client: &ssh.ClientConfig{
			User: cfg.User,
			Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
			// TODO: pin the node host key (known_hosts) for production.
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         sshDialTimeout,
		},
	}, nil
}

// PctExec runs argv inside the container as container-root via the scoped
// wrapper (`sudo <wrapper> exec <vmid> -- argv...`). argv is passed as a bare
// argument vector — callers needing shell features push a script and exec it.
// Returns combined stdout+stderr.
func (c *SSHClient) PctExec(vmid int, argv ...string) (string, error) {
	conn, err := ssh.Dial("tcp", c.addr, c.client)
	if err != nil {
		return "", fmt.Errorf("lxc ssh dial: %w", err)
	}
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	cmd := shellJoin(append([]string{"sudo", c.cfg.Wrapper, "exec", strconv.Itoa(vmid), "--"}, argv...))
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return string(out), fmt.Errorf("pct exec %d: %w: %s", vmid, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Push writes content into the container at dest with octal perms. The file is
// staged under the account home (the wrapper requires push sources there),
// pct-pushed, then the stage file is removed.
func (c *SSHClient) Push(vmid int, content []byte, perms, dest string) error {
	conn, err := ssh.Dial("tcp", c.addr, c.client)
	if err != nil {
		return fmt.Errorf("lxc ssh dial: %w", err)
	}
	defer conn.Close()
	sc, err := sftp.NewClient(conn)
	if err != nil {
		return fmt.Errorf("lxc sftp: %w", err)
	}
	defer sc.Close()

	stage := path.Join(c.cfg.StagingDir, fmt.Sprintf("stage-%d-%d", vmid, time.Now().UnixNano()))
	f, err := sc.Create(stage)
	if err != nil {
		return fmt.Errorf("stage create %s: %w", stage, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("stage write: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	defer func() { _ = sc.Remove(stage) }()

	sess, err := conn.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	cmd := shellJoin([]string{"sudo", c.cfg.Wrapper, "push", strconv.Itoa(vmid), stage, dest, "--perms", perms})
	if out, err := sess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("pct push %d -> %s: %w: %s", vmid, dest, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ReadFile returns the contents of a file inside the container via `cat`. A
// missing file yields empty content and no error, so callers can treat a
// not-yet-created config file as blank. Config files are text, so combined
// stdout is a faithful copy.
func (c *SSHClient) ReadFile(vmid int, srcPath string) ([]byte, error) {
	out, err := c.PctExec(vmid, "cat", srcPath)
	if err != nil {
		// `cat` of a missing file exits non-zero; that is not an error here.
		if strings.Contains(out, "No such file") {
			return nil, nil
		}
		return nil, err
	}
	return []byte(out), nil
}

// shellJoin single-quote-escapes each argument for a POSIX shell command line,
// so arbitrary values pass through the remote login shell unmangled.
func shellJoin(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}
