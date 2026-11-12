package bus

import (
	"golang.org/x/sys/windows"
	"os"
)

func attachmentAvailableSpace(_ *os.Root, directory string) (uint64, error) {
	path, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(path, &available, nil, nil)
	return available, err
}
