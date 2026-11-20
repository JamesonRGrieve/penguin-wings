// SPDX-License-Identifier: AGPL-3.0-or-later

package router

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/pelican/wings/environment/lxc"
	"github.com/pelican/wings/server"
	"github.com/pelican/wings/server/filesystem"
)

// Helpers for the file-manager branches that serve LXC servers from their
// container rather than from the Wings-local server directory.

// lxcEnvironment returns the server's LXC environment, if it has one.
func lxcEnvironment(s *server.Server) (*lxc.Environment, bool) {
	env, ok := s.Environment.(*lxc.Environment)
	return env, ok
}

// abortIfLXC refuses a file operation the LXC backend doesn't implement yet,
// instead of letting it act on the empty local directory and report success.
// It reports whether the request was aborted.
func abortIfLXC(c *gin.Context, s *server.Server) bool {
	if _, ok := lxcEnvironment(s); !ok {
		return false
	}
	c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{
		"error": lxc.ErrUnsupportedFileOp.Error(),
	})
	return true
}

// errLXCTooLarge is returned when a body is over the upload limit.
var errLXCTooLarge = errors.New("lxc: file is larger than the upload limit")

// readLimited reads r whole, failing once it passes limit bytes. The LXC
// channel pushes whole files, so the body is buffered, and the limit keeps an
// oversized request from exhausting Wings' memory.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errLXCTooLarge
	}
	return b, nil
}

// textExtensions are config and log formats the panel should open in its
// editor. Upstream sniffs file contents for the mimetype; reading every file
// in the container to list a directory would cost a round trip each, so the
// LXC listing goes by extension.
var textExtensions = map[string]bool{
	".cfg": true, ".conf": true, ".ini": true, ".properties": true, ".toml": true,
	".yml": true, ".yaml": true, ".json": true, ".txt": true, ".log": true,
	".xml": true, ".sh": true, ".env": true, ".md": true, ".csv": true, ".lua": true,
	".js": true, ".ts": true, ".py": true, ".html": true, ".css": true,
}

// lxcMimetype guesses an entry's mimetype from its name.
func lxcMimetype(e lxc.FileEntry) string {
	if e.IsDir() {
		return "inode/directory"
	}
	ext := strings.ToLower(path.Ext(e.Name()))
	if textExtensions[ext] {
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// lxcStats converts container entries to the Stat values the panel expects.
func lxcStats(entries []lxc.FileEntry) []filesystem.Stat {
	out := make([]filesystem.Stat, 0, len(entries))
	for _, e := range entries {
		out = append(out, filesystem.Stat{FileInfo: e, Mimetype: lxcMimetype(e)})
	}
	return out
}

// lxcReadSeeker wraps file contents for serveDownload.
func lxcReadSeeker(b []byte) io.ReadSeeker { return bytes.NewReader(b) }
