//go:build linux || darwin

package bus

import (
	"golang.org/x/sys/unix"
	"os"
)

func attachmentAvailableSpace(root *os.Root, _ string) (uint64, error) {
	directory, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &stats); err != nil {
		return 0, err
	}
	return uint64(stats.Bavail) * uint64(stats.Bsize), nil
}
