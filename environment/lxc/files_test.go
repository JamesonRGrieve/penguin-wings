// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestParseListing(t *testing.T) {
	t.Parallel()
	raw := "1024\t644\t1700000000\tregular file\t/home/container/server.properties\n" +
		"4096\t755\t1700000001\tdirectory\t/home/container/world\n" +
		"9\t777\t1700000002\tsymbolic link\t/home/container/latest.log\n" +
		"0\t644\t1700000003\tregular empty file\t/home/container/odd\tname\n"
	got, err := parseListing(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}
	if got[0].Name() != "server.properties" || got[0].Size() != 1024 || !got[0].Mode().IsRegular() || got[0].Mode().Perm() != 0o644 {
		t.Errorf("file entry = %+v", got[0])
	}
	if !got[1].IsDir() || got[1].Mode().Perm() != 0o755 {
		t.Errorf("dir entry = %+v", got[1])
	}
	if got[2].Mode()&fs.ModeSymlink == 0 {
		t.Errorf("symlink entry = %+v", got[2])
	}
	if got[3].Name() != "odd\tname" || !got[3].Mode().IsRegular() {
		t.Errorf("tab name entry = %+v", got[3])
	}
	if got[0].ModTime().Unix() != 1700000000 {
		t.Errorf("mtime = %v", got[0].ModTime())
	}
	if empty, err := parseListing(""); err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty listing = %v, %v; want non-nil empty slice", empty, err)
	}
	if _, err := parseListing("garbage\n"); err == nil {
		t.Error("malformed line parsed without error")
	}
}

func TestTarRoundTrip(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(src, "world", "region"), 0o755))
	must(os.WriteFile(filepath.Join(src, "eula.txt"), []byte("eula=true\n"), 0o644))
	must(os.WriteFile(filepath.Join(src, "world", "region", "r.0.0.mca"), []byte{0, 1, 2, 255}, 0o600))
	must(os.Symlink("eula.txt", filepath.Join(src, "link")))

	var buf bytes.Buffer
	must(writeTar(src, &buf))

	dst := t.TempDir()
	root, err := os.OpenRoot(dst)
	must(err)
	defer root.Close()
	must(extractTar(root, &buf))

	b, err := os.ReadFile(filepath.Join(dst, "world", "region", "r.0.0.mca"))
	must(err)
	if !bytes.Equal(b, []byte{0, 1, 2, 255}) {
		t.Errorf("binary file = %v", b)
	}
	if st, err := os.Stat(filepath.Join(dst, "world", "region", "r.0.0.mca")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", st, err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "eula.txt" {
		t.Errorf("symlink = %q, %v", l, err)
	}
}

func TestExtractTarRefusesEscape(t *testing.T) {
	t.Parallel()
	for name, hdr := range map[string]*tar.Header{
		"dotdot file":       {Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		"file through link": {Name: "out/evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "mirror")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if name == "file through link" {
				_ = tw.WriteHeader(&tar.Header{Name: "out", Typeflag: tar.TypeSymlink, Linkname: parent, Mode: 0o777})
			}
			_ = tw.WriteHeader(hdr)
			_, _ = tw.Write([]byte("x"))
			_ = tw.Close()

			root, err := os.OpenRoot(dst)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := extractTar(root, &buf); err == nil {
				t.Error("escaping entry extracted without error")
			}
			if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
				t.Error("file written outside the mirror")
			}
		})
	}
}
