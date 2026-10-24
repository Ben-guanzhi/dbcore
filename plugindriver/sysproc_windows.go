//go:build windows

package plugindriver

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a plugin's console from flashing on screen: driver plugins
// are background processes the user never chose to see.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windowsCreationNoWindow
}

const windowsCreationNoWindow = 0x08000000 // CREATE_NO_WINDOW
