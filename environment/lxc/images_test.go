// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import "testing"

func TestOCIArtifactName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref  string
		want string
	}{
		{"ghcr.io/pelican-eggs/yolks:java_21", "ghcr_io_pelican_eggs_yolks_java_21"},
		{"docker.io/library/alpine:3.20", "docker_io_library_alpine_3_20"},
		{"ghcr.io/pelican-eggs/steamcmd:debian", "ghcr_io_pelican_eggs_steamcmd_debian"},
		// Leading/trailing and runs of non-alphanumerics collapse to single _.
		{"://weird..ref--x:", "weird_ref_x"},
		{"UPPER/Case:TAG", "upper_case_tag"},
	}
	for _, c := range cases {
		if got := ociArtifactName(c.ref); got != c.want {
			t.Errorf("ociArtifactName(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
