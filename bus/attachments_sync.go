//go:build !windows

package bus

import "os"

func syncAttachmentDirectory(directory *os.File) error { return directory.Sync() }
