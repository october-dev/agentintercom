//go:build !linux && !darwin && !windows

package bus

import "os"

// The published Linux, macOS and Windows builds enforce a disk reserve. Other
// ports retain filesystem write errors and require operator disk monitoring.
func attachmentAvailableSpace(_ *os.Root, _ string) (uint64, error) { return ^uint64(0), nil }
