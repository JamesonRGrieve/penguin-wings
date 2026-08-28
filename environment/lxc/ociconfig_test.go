// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import "testing"

func TestParseImageRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in             string
		reg, repo, tag string
	}{
		{"ghcr.io/pelican-eggs/yolks:java_21", "ghcr.io", "pelican-eggs/yolks", "java_21"},
		{"ghcr.io/parkervcp/steamcmd:debian", "ghcr.io", "parkervcp/steamcmd", "debian"},
		{"ghcr.io/parkervcp/yolks:debian", "ghcr.io", "parkervcp/yolks", "debian"},
		{"nginx", "registry-1.docker.io", "library/nginx", "latest"},
		{"library/nginx:1.27", "registry-1.docker.io", "library/nginx", "1.27"},
		{"grafana/grafana:11.0.0", "registry-1.docker.io", "grafana/grafana", "11.0.0"},
		{"localhost:5000/my/app:dev", "localhost:5000", "my/app", "dev"},
		{"registry.example.com/team/app", "registry.example.com", "team/app", "latest"},
	}
	for _, c := range cases {
		reg, repo, tag := parseImageRef(c.in)
		if reg != c.reg || repo != c.repo || tag != c.tag {
			t.Errorf("parseImageRef(%q) = (%q,%q,%q); want (%q,%q,%q)",
				c.in, reg, repo, tag, c.reg, c.repo, c.tag)
		}
	}
}

func TestBearerParamRe(t *testing.T) {
	t.Parallel()
	challenge := `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:pelican-eggs/yolks:pull"`
	got := map[string]string{}
	for _, m := range bearerParamRe.FindAllStringSubmatch(challenge, -1) {
		got[m[1]] = m[2]
	}
	if got["realm"] != "https://ghcr.io/token" {
		t.Errorf("realm = %q", got["realm"])
	}
	if got["service"] != "ghcr.io" {
		t.Errorf("service = %q", got["service"])
	}
	if got["scope"] != "repository:pelican-eggs/yolks:pull" {
		t.Errorf("scope = %q", got["scope"])
	}
}
