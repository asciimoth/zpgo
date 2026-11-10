//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package zpgo

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func duplicateFile(file *os.File) (*pipeFile, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var pipe *pipeFile
	var dupErr error
	err = raw.Control(func(fd uintptr) { pipe, dupErr = duplicateFD(int(fd), false) })
	if err != nil {
		return nil, err
	}
	return pipe, dupErr
}

func duplicateFD(source int, inherited bool) (*pipeFile, error) {
	// F_DUPFD keeps owned descriptors away from stdin/stdout/stderr, even if
	// the application has closed them. ForkLock protects the CLOEXEC setup.
	// Use the libc wrapper on systems that do not permit raw fcntl syscalls.
	syscall.ForkLock.RLock()
	fd, err := unix.FcntlInt(uintptr(source), syscall.F_DUPFD, 3)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "zig-progress")
	var stat syscall.Stat_t
	err = syscall.Fstat(fd, &stat)
	if err == nil && stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		err = fmt.Errorf("not a pipe")
	}
	if err == nil {
		flags, flagErr := unix.FcntlInt(uintptr(fd), syscall.F_GETFL, 0)
		if flagErr != nil {
			err = flagErr
		} else if flags&syscall.O_ACCMODE == syscall.O_RDONLY {
			err = fmt.Errorf("pipe is not writable")
		}
	}
	if err == nil {
		err = syscall.SetNonblock(fd, true)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	// An inherited environment descriptor is otherwise not close-on-exec.
	// Do not pass the upstream channel to unrelated child processes.
	if inherited {
		syscall.CloseOnExec(source)
	}
	return &pipeFile{file: f, fd: fd}, nil
}

func progressPipe() (*pipeFile, *os.File, error) {
	// Construct os.File values while the descriptors are blocking. os.Pipe
	// registers pollable files whose Fd method can clear O_NONBLOCK each time
	// os/exec asks for the descriptor. These files use raw syscalls instead.
	var fds [2]int
	syscall.ForkLock.RLock()
	err := syscall.Pipe(fds[:])
	if err == nil {
		// A closed standard stream must stay closed. Otherwise ordinary I/O
		// or the child's standard stream mappings can use our private pipe.
		for i, fd := range fds {
			if fd < 3 {
				dup, dupErr := unix.FcntlInt(uintptr(fd), syscall.F_DUPFD, 3)
				if dupErr != nil {
					err = dupErr
					break
				}
				_ = syscall.Close(fd)
				fds[i] = dup
			}
			syscall.CloseOnExec(fds[i])
		}
		if err != nil {
			_ = syscall.Close(fds[0])
			_ = syscall.Close(fds[1])
		}
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	rfd, wfd := fds[0], fds[1]
	r, w := os.NewFile(uintptr(rfd), "progress-reader"), os.NewFile(uintptr(wfd), "progress-writer")
	err = syscall.SetNonblock(rfd, true)
	if err == nil {
		err = syscall.SetNonblock(wfd, true)
	}
	if err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, nil, err
	}
	return &pipeFile{file: r, fd: rfd}, w, nil
}

func (p *pipeFile) read(buf []byte) (int, error)  { return syscall.Read(p.fd, buf) }
func (p *pipeFile) write(buf []byte) (int, error) { return syscall.Write(p.fd, buf) }
func temporary(err error) bool {
	return err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EINTR
}
