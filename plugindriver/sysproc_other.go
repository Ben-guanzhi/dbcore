//go:build !windows

package plugindriver

import "os/exec"

// hideWindow is a Windows-only concern: elsewhere a child process never
// opens a window to hide.
func hideWindow(*exec.Cmd) {}
