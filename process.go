package zpgo

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Spawn starts cmd with a private progress pipe, like cmd.Start. Configure cmd
// normally, then call Spawn instead of Start or Run, and call cmd.Wait yourself.
// It preserves stdin, stdout, stderr, context cancellation, and ExtraFiles.
// Env and ExtraFiles are copied temporarily and restored before return.
// Use one process per active node. A failed start leaves the node reusable.
// Disabled and ended nodes still start commands, with ZIG_PROGRESS removed.
func (n Node) Spawn(cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("zpgo: nil command")
	}
	return n.withChild(func(file *os.File) error {
		env, files := cmd.Env, cmd.ExtraFiles
		defer func() { cmd.Env, cmd.ExtraFiles = env, files }()
		childEnvironment := env
		if childEnvironment == nil {
			childEnvironment = cmd.Environ()
		}
		// Cmd.Environ silently drops invalid entries. Preserve Start's rejection
		// of NUL bytes, including entries that childEnv would remove.
		if err := validateChildEnv(childEnvironment); err != nil {
			return err
		}
		cmd.Env = childEnv(childEnvironment, -1)
		if file != nil {
			fd := 3 + len(files)
			if fd > 65535 {
				return fmt.Errorf("zpgo: child progress descriptor exceeds 65535")
			}
			cmd.Env = childEnv(cmd.Env, fd)
			cmd.ExtraFiles = append(append([]*os.File(nil), files...), file)
		}
		return cmd.Start()
	})
}

// withChild serializes node lifetime with process creation. Only a successful
// start attaches the reader. The child owns its inherited writer after exec.
func (n Node) withChild(start func(*os.File) error) error {
	if n.p == nil {
		return start(nil)
	}
	p := n.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if !n.s.active {
		return start(nil)
	}
	if n.s.child != nil {
		return ErrProcessAttached
	}
	reader, writer, err := progressPipe()
	if err != nil {
		return err
	}
	defer writer.Close()
	if err := start(writer); err != nil {
		_ = reader.close()
		return err
	}
	n.s.child = &childPipe{pipe: reader}
	return nil
}

func childEnv(env []string, fd int) []string {
	return childEnvCase(env, fd, runtime.GOOS == "windows")
}

func validateChildEnv(env []string) error {
	if runtime.GOOS != "plan9" {
		for _, item := range env {
			if strings.IndexByte(item, 0) >= 0 {
				return fmt.Errorf("zpgo: environment variable contains NUL")
			}
		}
	}
	return nil
}

func childEnvCase(env []string, fd int, caseInsensitive bool) []string {
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if key != "ZIG_PROGRESS" && !(caseInsensitive && strings.EqualFold(key, "ZIG_PROGRESS")) {
			result = append(result, item)
		}
	}
	if fd >= 0 {
		result = append(result, "ZIG_PROGRESS="+strconv.Itoa(fd))
	}
	return result
}
