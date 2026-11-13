package main

import "golang.org/x/sys/unix"

// processLaunchRecord returns what macOS reports to same-user processes about
// how pid was started (KERN_PROCARGS2): its argument count, executable path,
// arguments and the environment it was started with.
func processLaunchRecord(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}
