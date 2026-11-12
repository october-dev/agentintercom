package main

import (
	"fmt"
	"os"
)

// processLaunchRecord returns what Linux reports to same-user processes about
// how pid was started: its arguments and the environment it was started with.
func processLaunchRecord(pid int) ([]byte, error) {
	arguments, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	environment, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	return append(arguments, environment...), err
}
