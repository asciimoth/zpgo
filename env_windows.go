package zpgo

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func defaultChildEnv(cmd *exec.Cmd) ([]string, error) {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Token != 0 {
		return windows.Token(cmd.SysProcAttr.Token).Environ(false)
	}
	return os.Environ(), nil
}
