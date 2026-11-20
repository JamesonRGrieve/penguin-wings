// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Backups and restores for LXC servers. Upstream builds a backup archive from
// the Wings-local server directory and restores into it. An LXC server's files
// live in its container, so the server mirrors the container's data dir into
// that local directory before a backup (PullData) and pushes the restored
// directory back afterwards (PushData). Upstream's archive and restore code then
// runs unchanged on the mirror.

// mirrorKeep lists data-dir entries Penguin owns that a restore must never
// delete: without the run-script the container can't boot the game.
var mirrorKeep = []string{path.Base(runScriptPath)}

// PullData copies the container's data dir into localDir, which must exist and
// should be empty. The container streams a tar of its data dir; entries are
// written through an os.Root so a crafted archive can't escape localDir.
func (e *Environment) PullData(ctx context.Context, localDir string) error {
	if e.ssh == nil {
		return fmt.Errorf("lxc: ssh channel not configured")
	}
	root, err := os.OpenRoot(localDir)
	if err != nil {
		return fmt.Errorf("lxc: open mirror dir: %w", err)
	}
	defer root.Close()
	return e.withRunning(ctx, func() error {
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			err := extractTar(root, pr)
			// Drain whatever is left so the remote side can finish writing.
			_, _ = io.Copy(io.Discard, pr)
			done <- err
		}()
		_, execErr := e.ssh.ExecStream(e.vmid, nil, pw, "tar", "-C", dataDir, "-cf", "-", ".")
		_ = pw.CloseWithError(execErr)
		extractErr := <-done
		if execErr != nil {
			return fmt.Errorf("lxc: read container data: %w", execErr)
		}
		return extractErr
	})
}

// PushData copies localDir into the container's data dir and hands the files to
// the container user. With wipe set, the data dir is emptied first (except the
// entries Penguin owns), matching a truncating restore.
func (e *Environment) PushData(ctx context.Context, localDir string, wipe bool) error {
	if e.ssh == nil {
		return fmt.Errorf("lxc: ssh channel not configured")
	}
	return e.withRunning(ctx, func() error {
		if wipe {
			args := []string{"find", dataDir, "-mindepth", "1", "-maxdepth", "1"}
			for _, k := range mirrorKeep {
				args = append(args, "!", "-name", k)
			}
			args = append(args, "-exec", "rm", "-rf", "--", "{}", "+")
			if out, err := e.ssh.PctExec(e.vmid, args...); err != nil {
				return fmt.Errorf("lxc: clear data dir: %w\n%s", err, out)
			}
		}
		pr, pw := io.Pipe()
		go func() { _ = pw.CloseWithError(writeTar(localDir, pw)) }()
		_, err := e.ssh.ExecStream(e.vmid, pr, io.Discard, "tar", "-C", dataDir, "-xf", "-")
		_ = pr.CloseWithError(err)
		if err != nil {
			return fmt.Errorf("lxc: write container data: %w", err)
		}
		if out, err := e.ssh.PctExec(e.vmid, "chown", "-R", containerUser+":"+containerUser, dataDir); err != nil {
			return fmt.Errorf("lxc: chown data dir: %w\n%s", err, out)
		}
		return nil
	})
}

// extractTar writes a tar stream into root. Only directories, regular files and
// symlinks are restored; os.Root refuses any path or link that leaves the root.
func extractTar(root *os.Root, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lxc: read tar: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		mode := fs.FileMode(hdr.Mode) & fs.ModePerm
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, mode|0o700); err != nil {
				return fmt.Errorf("lxc: mkdir %s: %w", name, err)
			}
		case tar.TypeReg:
			if dir := path.Dir(name); dir != "." {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("lxc: mkdir %s: %w", dir, err)
				}
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("lxc: create %s: %w", name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return fmt.Errorf("lxc: write %s: %w", name, err)
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if dir := path.Dir(name); dir != "." {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("lxc: mkdir %s: %w", dir, err)
				}
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return fmt.Errorf("lxc: symlink %s: %w", name, err)
			}
		default:
			// Devices, fifos and hard links have no place in a game's data dir.
		}
	}
}

// writeTar streams localDir as a tar archive with paths relative to it. Symlinks
// are archived as links, never followed.
func writeTar(localDir string, w io.Writer) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(localDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(localDir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = "./" + filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			_ = f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
