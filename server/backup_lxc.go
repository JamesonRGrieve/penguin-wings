// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"emperror.dev/errors"

	"github.com/pelican/wings/environment/lxc"
)

// An LXC server's files live in its container, not in the Wings-local server
// directory that upstream's backup code archives and restores into. These
// helpers use that directory as a temporary mirror: filled from the container
// before a backup, pushed into the container after a restore, and emptied after
// either. For any other environment they do nothing.

// mirrorLXCData fills the local server directory from the container.
func (s *Server) mirrorLXCData() error {
	env, ok := s.Environment.(*lxc.Environment)
	if !ok {
		return nil
	}
	if err := s.Filesystem().TruncateRootDirectory(); err != nil {
		return errors.WrapIf(err, "server/backup: clear lxc mirror")
	}
	s.Log().Info("copying container files for backup")
	return errors.WrapIf(env.PullData(s.Context(), s.Filesystem().Path()), "server/backup: copy container files")
}

// dropLXCMirror empties the local server directory again.
func (s *Server) dropLXCMirror() {
	if _, ok := s.Environment.(*lxc.Environment); !ok {
		return
	}
	if err := s.Filesystem().TruncateRootDirectory(); err != nil {
		s.Log().WithField("error", err).Warn("failed to clear the lxc backup mirror")
	}
}

// pushLXCRestore writes the restored local directory into the container, then
// empties the mirror. wipe clears the container's data dir first.
func (s *Server) pushLXCRestore(wipe bool) error {
	env, ok := s.Environment.(*lxc.Environment)
	if !ok {
		return nil
	}
	defer s.dropLXCMirror()
	s.Log().Info("copying restored files into the container")
	return errors.WrapIf(env.PushData(s.Context(), s.Filesystem().Path(), wipe), "server/backup: restore into container")
}
