//go:build !windows

package pluginrunner

import "syscall"

func sigterm() syscall.Signal { return syscall.SIGTERM }
