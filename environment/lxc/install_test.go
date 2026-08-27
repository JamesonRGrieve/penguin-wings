// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"strings"
	"testing"
)

func TestSingleQuote(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"simple":     "'simple'",
		"with space": "'with space'",
		"it's":       `'it'\''s'`,
		"":           "''",
	}
	for in, want := range cases {
		if got := singleQuote(in); got != want {
			t.Errorf("singleQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellJoin(t *testing.T) {
	t.Parallel()
	got := shellJoin([]string{"sudo", "penguin-pct", "exec", "100007", "--", "bash", "/tmp/x"})
	want := `'sudo' 'penguin-pct' 'exec' '100007' '--' 'bash' '/tmp/x'`
	if got != want {
		t.Errorf("shellJoin = %q, want %q", got, want)
	}
	if got := shellJoin([]string{"a'b"}); got != `'a'\''b'` {
		t.Errorf("shellJoin quote-escape = %q, want %q", got, `'a'\''b'`)
	}
}

func TestInstallWrapper(t *testing.T) {
	t.Parallel()
	w := installWrapper(map[string]string{"SERVER_JARFILE": "server.jar", "MSG": "hi there"}, []string{"1.1.1.1"})
	if !strings.Contains(w, "echo 'nameserver 1.1.1.1' >> /etc/resolv.conf") {
		t.Errorf("wrapper missing resolver:\n%s", w)
	}
	if !strings.Contains(w, "ln -sfn "+dataDir+" "+serverDir) {
		t.Errorf("wrapper missing /mnt/server symlink:\n%s", w)
	}
	if !strings.Contains(w, "export MSG='hi there'") {
		t.Errorf("wrapper missing quoted export:\n%s", w)
	}
	if !strings.Contains(w, "export SERVER_JARFILE='server.jar'") {
		t.Error("wrapper missing jarfile export")
	}
	if !strings.Contains(w, "bash "+eggScriptPath) {
		t.Error("wrapper missing egg script invocation")
	}
	// exports are emitted in sorted order (MSG before SERVER_JARFILE).
	if strings.Index(w, "export MSG=") > strings.Index(w, "export SERVER_JARFILE=") {
		t.Error("exports not sorted deterministically")
	}
}

func TestRunScript(t *testing.T) {
	t.Parallel()
	r := runScript("java -Xms128M -jar server.jar", map[string]string{"RCON_PORT": "25575"})
	if !strings.Contains(r, "cd "+dataDir) {
		t.Errorf("run-script missing cd:\n%s", r)
	}
	if !strings.Contains(r, "export RCON_PORT='25575'") {
		t.Errorf("run-script missing env export:\n%s", r)
	}
	if !strings.Contains(r, "java -Xms128M -jar server.jar") {
		t.Errorf("run-script missing invocation:\n%s", r)
	}
}
