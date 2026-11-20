// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// File-manager operations for LXC servers. The Docker backend serves these from
// a Wings-local volume through server/filesystem; an LXC server's files live in
// its container, so they are reached over the scoped node channel instead. Every
// path is confined to dataDir by containerPath, because the channel runs as
// container-root and nothing downstream would stop "../".

// ErrUnsupportedFileOp is returned for file-manager operations the LXC backend
// does not implement yet. It is better to refuse than to act on the empty
// Wings-local directory and report success.
var ErrUnsupportedFileOp = errors.New("lxc: this file operation is not supported for LXC servers yet")

// FileEntry is one directory entry read from inside the container. It satisfies
// os.FileInfo so the router can hand it to the upstream filesystem.Stat.
type FileEntry struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func (f FileEntry) Name() string       { return f.name }
func (f FileEntry) Size() int64        { return f.size }
func (f FileEntry) Mode() fs.FileMode  { return f.mode }
func (f FileEntry) ModTime() time.Time { return f.modTime }
func (f FileEntry) IsDir() bool        { return f.mode.IsDir() }
func (f FileEntry) Sys() any           { return nil }

// listFormat is the `stat -c` format: size, octal perms, mtime, type, name. The
// name is last so a name containing tabs still parses. Both GNU and busybox stat
// support these directives.
const listFormat = "%s\t%a\t%Y\t%F\t%n"

// ListDirectory returns the entries of a directory under the data dir.
func (e *Environment) ListDirectory(ctx context.Context, relPath string) ([]FileEntry, error) {
	if e.ssh == nil {
		return nil, fmt.Errorf("lxc: ssh channel not configured")
	}
	dir, err := containerPath(relPath)
	if err != nil {
		return nil, err
	}
	var out []FileEntry
	err = e.withRunning(ctx, func() error {
		var buf bytes.Buffer
		stderr, err := e.ssh.ExecStream(e.vmid, nil, &buf,
			"find", dir, "-mindepth", "1", "-maxdepth", "1", "-exec", "stat", "-c", listFormat, "{}", "+")
		if err != nil {
			if strings.Contains(stderr, "No such file") {
				return os.ErrNotExist
			}
			return err
		}
		out, err = parseListing(buf.String())
		return err
	})
	return out, err
}

// parseListing parses listFormat lines into entries.
func parseListing(raw string) ([]FileEntry, error) {
	out := make([]FileEntry, 0)
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) != 5 {
			return nil, fmt.Errorf("lxc: unexpected stat line %q", line)
		}
		size, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("lxc: stat size %q: %w", parts[0], err)
		}
		perm, err := strconv.ParseUint(parts[1], 8, 32)
		if err != nil {
			return nil, fmt.Errorf("lxc: stat mode %q: %w", parts[1], err)
		}
		mtime, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("lxc: stat mtime %q: %w", parts[2], err)
		}
		mode := fs.FileMode(perm) & fs.ModePerm
		switch kind := parts[3]; {
		case kind == "directory":
			mode |= fs.ModeDir
		case strings.Contains(kind, "symbolic link"):
			mode |= fs.ModeSymlink
		case kind == "fifo":
			mode |= fs.ModeNamedPipe
		case strings.Contains(kind, "socket"):
			mode |= fs.ModeSocket
		case strings.Contains(kind, "regular"):
		default:
			mode |= fs.ModeIrregular
		}
		out = append(out, FileEntry{name: path.Base(parts[4]), size: size, mode: mode, modTime: time.Unix(mtime, 0)})
	}
	return out, nil
}

// CreateDirectory makes a directory (and parents) under the data dir, owned by
// the container user.
func (e *Environment) CreateDirectory(ctx context.Context, relPath string) error {
	p, err := containerPath(relPath)
	if err != nil {
		return err
	}
	return e.withRunning(ctx, func() error {
		if out, err := e.ssh.PctExec(e.vmid, "mkdir", "-p", "--", p); err != nil {
			return fmt.Errorf("mkdir %s: %w\n%s", p, err, out)
		}
		return e.chownToContainerUser(p)
	})
}

// Rename moves a file or directory within the data dir. Like the Docker
// backend it refuses to overwrite an existing target.
func (e *Environment) Rename(ctx context.Context, from, to string) error {
	src, err := containerPath(from)
	if err != nil {
		return err
	}
	dst, err := containerPath(to)
	if err != nil {
		return err
	}
	if src == dataDir || dst == dataDir {
		return ErrPathOutsideDataDir
	}
	return e.withRunning(ctx, func() error {
		if _, err := e.ssh.PctExec(e.vmid, "test", "!", "-e", dst); err != nil {
			return fs.ErrExist
		}
		if dir := path.Dir(dst); dir != dataDir {
			if out, err := e.ssh.PctExec(e.vmid, "mkdir", "-p", "--", dir); err != nil {
				return fmt.Errorf("mkdir %s: %w\n%s", dir, err, out)
			}
		}
		if out, err := e.ssh.PctExec(e.vmid, "mv", "--", src, dst); err != nil {
			return fmt.Errorf("mv %s: %w\n%s", src, err, out)
		}
		return nil
	})
}

// Delete removes files and directories under the data dir. The data dir itself
// can't be deleted. With recursive false a directory is refused rather than
// removed: the caller uses that when the egg has a file denylist, because the
// Docker backend keeps denylisted files inside a deleted directory and a plain
// rm -r can't.
func (e *Environment) Delete(ctx context.Context, recursive bool, relPaths ...string) error {
	targets := make([]string, 0, len(relPaths))
	for _, rp := range relPaths {
		p, err := containerPath(rp)
		if err != nil {
			return err
		}
		if p == dataDir {
			return ErrPathOutsideDataDir
		}
		targets = append(targets, p)
	}
	if len(targets) == 0 {
		return nil
	}
	return e.withRunning(ctx, func() error {
		flags := "-f"
		if recursive {
			flags = "-rf"
		}
		if out, err := e.ssh.PctExec(e.vmid, append([]string{"rm", flags, "--"}, targets...)...); err != nil {
			return fmt.Errorf("rm: %w\n%s", err, out)
		}
		return nil
	})
}

// chownToContainerUser hands a path to the user the egg image runs the game as,
// so the game can rewrite files the panel created. Must run inside withRunning.
func (e *Environment) chownToContainerUser(p string) error {
	if out, err := e.ssh.PctExec(e.vmid, "chown", containerUser+":"+containerUser, p); err != nil {
		return fmt.Errorf("chown %s: %w\n%s", p, err, out)
	}
	return nil
}
