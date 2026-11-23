// Package fileutil 处理文件属主。
package fileutil

import (
	"os"
	"path/filepath"
)

func OwnedBy(fi os.FileInfo) (uid, gid int, ok bool) { return ownership(fi) }

func Ownership(path string) (uid, gid int, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return -1, -1, false
	}
	return ownership(fi)
}

func SetOwner(path string, uid, gid int) {
	if uid < 0 || gid < 0 {
		return
	}
	_ = os.Chown(path, uid, gid)
}

// 新建目录继承最近祖先的属主。
func MkdirAllOwned(dir string, perm os.FileMode) error {
	dir = filepath.Clean(dir)

	var missing []string
	existing := dir
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		missing = append(missing, existing)
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}

	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}

	uid, gid, ok := Ownership(existing)
	if !ok {
		return nil
	}
	for i := len(missing) - 1; i >= 0; i-- {
		SetOwner(missing[i], uid, gid)
	}
	return nil
}
