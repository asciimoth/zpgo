//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package zpgo

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProgressPipeClosedStandardStreams(t *testing.T) {
	const key = "ZPGO_TEST_CLOSED_STDIO"
	if value := os.Getenv(key); value != "" {
		mask := 7
		if value != "exhausted" {
			var err error
			mask, err = strconv.Atoi(value)
			if err != nil || mask < 1 || mask > 7 {
				t.Fatal("invalid standard stream mask")
			}
		}
		// Restore the streams before the test runner writes its coverage and
		// result output. Only the helper process changes these descriptors.
		for fd := 0; fd < 3; fd++ {
			if mask&(1<<fd) != 0 {
				saved, err := syscall.Dup(fd)
				if err != nil {
					t.Fatal(err)
				}
				defer func(fd, saved int) {
					_ = syscall.Close(fd)
					dup, err := unix.FcntlInt(uintptr(saved), syscall.F_DUPFD, fd)
					_ = syscall.Close(saved)
					if err != nil || dup != fd {
						t.Errorf("restore standard stream %d: descriptor %d, %v", fd, dup, err)
					}
				}(fd, saved)
			}
		}
		for fd := 0; fd < 3; fd++ {
			if mask&(1<<fd) != 0 {
				_ = syscall.Close(fd)
			}
		}
		if value == "exhausted" {
			var limit syscall.Rlimit
			if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
				t.Fatal(err)
			}
			original := limit
			defer syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original)
			limit.Cur = 3
			if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
				t.Fatal(err)
			}
		}
		r, w, err := progressPipe()
		if value == "exhausted" {
			if err == nil {
				t.Fatal("created a pipe without space for private descriptors")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			defer r.close()
			defer w.Close()
			if r.fd < 3 || w.Fd() < 3 {
				t.Fatal("progress pipe reused a standard stream")
			}
		}
		for fd := 0; fd < 3; fd++ {
			if mask&(1<<fd) != 0 {
				var stat syscall.Stat_t
				if err := syscall.Fstat(fd, &stat); err != syscall.EBADF {
					t.Fatalf("standard stream %d is open: %v", fd, err)
				}
			}
		}
		if value == "exhausted" {
			return
		}
		for _, fd := range []uintptr{uintptr(r.fd), w.Fd()} {
			flags, err := unix.FcntlInt(fd, syscall.F_GETFD, 0)
			if err != nil || flags&syscall.FD_CLOEXEC == 0 {
				t.Fatalf("pipe descriptor %d is not close-on-exec: %v", fd, err)
			}
			flags, err = unix.FcntlInt(fd, syscall.F_GETFL, 0)
			if err != nil || flags&syscall.O_NONBLOCK == 0 {
				t.Fatalf("pipe descriptor %d is blocking: %v", fd, err)
			}
		}
		if _, err := syscall.Write(int(w.Fd()), []byte{42}); err != nil {
			t.Fatal(err)
		}
		var buf [1]byte
		if n, err := r.read(buf[:]); err != nil || n != 1 || buf[0] != 42 {
			t.Fatalf("pipe transfer: %d %v %v", n, buf, err)
		}
		return
	}
	for _, value := range []string{"1", "2", "3", "4", "5", "6", "7", "exhausted"} {
		t.Run(value, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProgressPipeClosedStandardStreams$")
			cmd.Env = append(os.Environ(), key+"="+value, "GORACE=atexit_sleep_ms=0")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("closed standard streams: %v\n%s", err, output)
			}
		})
	}
}
