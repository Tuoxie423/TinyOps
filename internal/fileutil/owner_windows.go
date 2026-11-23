//go:build windows

package fileutil

import "os"

func ownership(fi os.FileInfo) (uid, gid int, ok bool) {
	return -1, -1, false
}
