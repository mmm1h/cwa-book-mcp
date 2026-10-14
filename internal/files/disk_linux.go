//go:build linux

package files

import (
	"os"
	"syscall"
)

func diskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	e := syscall.Statfs(path, &st)
	return st.Bavail * uint64(st.Bsize), e
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
