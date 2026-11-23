//go:build !windows

package files

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func uidGid(t *testing.T, path string) (int, int) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s 没有 Stat_t", path)
	}
	return int(st.Uid), int(st.Gid)
}

func TestSavePreservesExistingOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("需要 root 才能 chown 给其他 uid")
	}
	s, dir := newTestService(t)
	target := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, 4242, 4242); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := s.Save("app", "app.conf", strings.NewReader("new")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	uid, gid := uidGid(t, target)
	if uid != 4242 || gid != 4242 {
		t.Fatalf("覆盖后属主 = %d:%d, want 4242:4242", uid, gid)
	}
	uid, gid = uidGid(t, target+".bak")
	if uid != 4242 || gid != 4242 {
		t.Fatalf("备份属主 = %d:%d, want 4242:4242", uid, gid)
	}
}

func TestSaveInheritsDirectoryOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("需要 root 才能 chown 给其他 uid")
	}
	s, dir := newTestService(t)
	if err := os.Chown(dir, 4242, 4242); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := s.Save("app", "sub/new.txt", strings.NewReader("x")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	uid, gid := uidGid(t, filepath.Join(dir, "sub"))
	if uid != 4242 || gid != 4242 {
		t.Fatalf("新建目录属主 = %d:%d, want 4242:4242", uid, gid)
	}
	uid, gid = uidGid(t, filepath.Join(dir, "sub", "new.txt"))
	if uid != 4242 || gid != 4242 {
		t.Fatalf("新文件属主 = %d:%d, want 4242:4242", uid, gid)
	}
}
