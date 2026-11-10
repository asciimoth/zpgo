//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package zpgo

import (
	"fmt"
	"os"
	"syscall"
)

// ForkExec calls syscall.ForkExec with a private progress pipe attached to this
// node. Its arguments, PID, errors, and wait semantics follow syscall.ForkExec.
// It copies attr, Env, and Files and does not change the caller's values. A nil
// Env is empty, as in syscall.ForkExec; pass os.Environ() to inherit it. Files
// omitted from attr are closed in the child, including standard streams.
// The parent must reap the child (for example, with syscall.Wait4), then End the
// node. Raw fork with continued Go execution is not supported by the Go runtime.
func (n Node) ForkExec(argv0 string, argv []string, attr *syscall.ProcAttr) (int, error) {
	pid := 0
	err := n.withChild(func(file *os.File) error {
		var child syscall.ProcAttr
		if attr != nil {
			child = *attr
		}
		child.Env = childEnv(child.Env, -1)
		child.Files = append([]uintptr(nil), child.Files...)
		if file != nil {
			// Keep the progress channel separate from the standard streams.
			for len(child.Files) < 3 {
				child.Files = append(child.Files, ^uintptr(0))
			}
			fd := len(child.Files)
			if fd > 65535 {
				return fmt.Errorf("zpgo: child progress descriptor exceeds 65535")
			}
			child.Env = childEnv(child.Env, fd)
			child.Files = append(child.Files, file.Fd())
		}
		var err error
		pid, err = syscall.ForkExec(argv0, argv, &child)
		return err
	})
	return pid, err
}
