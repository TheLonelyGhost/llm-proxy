//go:build windows

package pluginrunner

import "os"

// Windows does not support SIGTERM via Signal; we use os.Kill instead.
func sigterm() os.Signal { return os.Kill }
