//go:build !windows

package zpgo

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Match exec.Cmd's default environment without Cmd.Environ, which discards
// setup errors. Cmd.Start still handles duplicate keys and critical variables.
func defaultChildEnv(cmd *exec.Cmd) ([]string, error) {
	env := os.Environ()
	if cmd.Dir != "" && runtime.GOOS != "plan9" {
		pwd, err := filepath.Abs(cmd.Dir)
		if err != nil {
			return nil, err
		}
		env = append(env, "PWD="+pwd)
	}
	return env, nil
}
