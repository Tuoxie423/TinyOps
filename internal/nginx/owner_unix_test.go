//go:build !windows

package nginx

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSavePreservesConfigOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("需要 root 才能 chown 给其他 uid")
	}
	s, root := newTestService(t)
	path := filepath.Join(root, "conf.d", "site.conf")
	write(t, path, "old")
	if err := os.Chown(path, 4242, 4242); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Save("conf.d", "site.conf", "new"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("没有 Stat_t")
	}
	if int(st.Uid) != 4242 || int(st.Gid) != 4242 {
		t.Fatalf("属主 = %d:%d, want 4242:4242", st.Uid, st.Gid)
	}
	bfi, err := os.Stat(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	bst := bfi.Sys().(*syscall.Stat_t)
	if int(bst.Uid) != 4242 || int(bst.Gid) != 4242 {
		t.Fatalf("备份属主 = %d:%d, want 4242:4242", bst.Uid, bst.Gid)
	}
}
