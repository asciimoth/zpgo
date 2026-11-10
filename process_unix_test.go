//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package zpgo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func helperEnv(mode string) []string {
	// ForkExec preserves duplicate environment entries. Go reads the first
	// entry, whereas os/exec keeps the last. Remove old control entries before
	// adding new ones so a worker cannot repeat its parent's spawn operation.
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "ZPGO_TEST_HELPER", "ZPGO_TEST_DEPTH", "ZPGO_ROOT_NAME", "GORACE":
			continue
		}
		env = append(env, entry)
	}
	depth, _ := strconv.Atoi(os.Getenv("ZPGO_TEST_DEPTH"))
	return append(env, "ZPGO_TEST_HELPER="+mode, "ZPGO_TEST_DEPTH="+strconv.Itoa(depth+1), "GORACE=atexit_sleep_ms=0")
}

func helperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessHelper$")
	cmd.Env = helperEnv(mode)
	return cmd
}

// The helper is re-executed to test actual descriptor and environment transfer.
func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("ZPGO_TEST_HELPER")
	if mode == "" {
		return
	}
	if err := runHelper(mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runHelper(mode string) error {
	depth, err := strconv.Atoi(os.Getenv("ZPGO_TEST_DEPTH"))
	if err != nil || depth < 1 || depth > 3 {
		return fmt.Errorf("invalid helper depth: %q", os.Getenv("ZPGO_TEST_DEPTH"))
	}
	if mode == "noenv" {
		if _, ok := os.LookupEnv("ZIG_PROGRESS"); ok {
			return fmt.Errorf("inherited upstream ZIG_PROGRESS")
		}
		return nil
	}
	if mode == "wait" {
		fmt.Println("ready")
		var b [1]byte
		_, err := os.Stdin.Read(b[:])
		return err
	}
	if mode == "io" {
		fd, ok := environmentFD(os.Getenv("ZIG_PROGRESS"))
		if !ok || fd != 4 {
			return fmt.Errorf("progress FD = %d (%v), want 4", fd, ok)
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		if errno != 0 || flags&syscall.O_NONBLOCK == 0 {
			return fmt.Errorf("child pipe is blocking")
		}
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "out:%s", input)
		fmt.Fprintf(os.Stderr, "err:%s", os.Getenv("ZPGO_EXTRA_ENV"))
		extra := os.NewFile(3, "extra")
		if _, err := extra.WriteString("extra preserved"); err != nil {
			return err
		}
		extra.Close()
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if cwd != os.Getenv("ZPGO_DIR") {
			return fmt.Errorf("directory = %s", cwd)
		}
	}
	size, _ := strconv.Atoi(os.Getenv("ZPGO_NAME_SIZE"))
	name := os.Getenv("ZPGO_ROOT_NAME")
	if name == "" {
		name = "child"
	}
	opts := Options{Manual: true, RootName: name, NameSize: size}
	if mode == "automatic" {
		opts.Manual = false
		opts.RefreshInterval = time.Millisecond
	}
	if mode == "scenario" {
		opts.RootName = "parent"
		opts.EstimatedTotalItems = 2
	}
	p, err := New(opts)
	if err != nil {
		return err
	}
	defer p.Close()
	if !p.Enabled() {
		return fmt.Errorf("helper reporter disabled")
	}
	flush := func() error {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := p.Flush()
			if !errors.Is(err, ErrWouldBlock) {
				return err
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("flush timeout")
			}
			time.Sleep(time.Millisecond)
		}
	}
	switch mode {
	case "report", "io", "automatic":
		p.Root().SetEstimatedTotalItems(10)
		p.Root().SetCompletedItems(6)
		p.Root().Start("leaf", 9).Add(4)
		if mode == "automatic" {
			time.Sleep(20 * time.Millisecond)
		}
	case "nested-spawn", "nested-fork":
		n := p.Root().Start("worker", 0)
		env := append(helperEnv("report"), "ZPGO_ROOT_NAME=grandchild")
		if mode == "nested-spawn" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$")
			cmd.Env = env
			if err := n.Spawn(cmd); err != nil {
				return err
			}
			if err := cmd.Wait(); err != nil {
				return err
			}
		} else {
			pid, err := n.ForkExec(os.Args[0], []string{os.Args[0], "-test.run=^TestProcessHelper$"}, &syscall.ProcAttr{Env: env, Files: []uintptr{0, 1, 2}})
			if err != nil {
				return err
			}
			if err := waitPID(pid); err != nil {
				return err
			}
		}
	case "scenario":
		a, b := p.Root().Start("first", 0), p.Root().Start("second", 0)
		cmd := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$")
		cmd.Env = append(helperEnv("nested-fork"), "ZPGO_ROOT_NAME=spawned")
		if err := a.Spawn(cmd); err != nil {
			return err
		}
		pid, err := b.ForkExec(os.Args[0], []string{os.Args[0], "-test.run=^TestProcessHelper$"}, &syscall.ProcAttr{
			Env: append(helperEnv("nested-spawn"), "ZPGO_ROOT_NAME=forked"), Files: []uintptr{0, 1, 2},
		})
		if err != nil {
			_ = cmd.Wait()
			return err
		}
		if err := cmd.Wait(); err != nil {
			return err
		}
		if err := waitPID(pid); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
		a.End()
		if err := flush(); err != nil {
			return err
		}
		b.End()
		fmt.Fprintln(os.Stdout, "stdout unchanged")
		fmt.Fprintln(os.Stderr, "stderr unchanged")
	case "native":
		path := os.Getenv("ZPGO_ZPG")
		n := p.Root().Start("native task", 0)
		cmd := exec.Command(path, "--report", "3", "8", "--name", "native leaf")
		if err := n.Spawn(cmd); err != nil {
			return err
		}
		if err := cmd.Wait(); err != nil {
			return err
		}
	case "capacity":
		nodes := make([]Node, MaxNodes-1)
		for i := range nodes {
			nodes[i] = p.Root().Start(fmt.Sprintf("task-%d", i), 1)
		}
		p.Root().Start("overflow", 0).End()
		if err := flush(); err != nil {
			return err
		}
		for _, n := range nodes {
			n.End()
		}
		n := p.Root().Start("replacement", 2)
		n.CompleteOne()
		if err := flush(); err != nil {
			return err
		}
		n.End()
	default:
		return fmt.Errorf("unknown helper mode %q", mode)
	}
	if err := flush(); err != nil {
		return err
	}
	return p.Close()
}

func waitPID(pid int) error {
	var status syscall.WaitStatus
	deadline := time.Now().Add(10 * time.Second)
	for {
		waited, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if waited == 0 {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				_, _ = syscall.Wait4(pid, &status, 0, nil)
				return fmt.Errorf("child %d wait timeout", pid)
			}
			time.Sleep(time.Millisecond)
			continue
		}
		if !status.Exited() || status.ExitStatus() != 0 {
			return fmt.Errorf("child status: %v", status)
		}
		return nil
	}
}

func TestHelperEnvironmentAndDepth(t *testing.T) {
	t.Setenv("ZPGO_TEST_HELPER", "scenario")
	t.Setenv("ZPGO_TEST_DEPTH", "2")
	t.Setenv("ZPGO_ROOT_NAME", "inherited")
	t.Setenv("GORACE", "old")
	env := helperEnv("report")
	seen := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate environment key %q", key)
		}
		seen[key] = value
	}
	if seen["ZPGO_TEST_HELPER"] != "report" || seen["ZPGO_TEST_DEPTH"] != "3" || seen["ZPGO_ROOT_NAME"] != "" {
		t.Fatal("helper inherited old control values")
	}
	for _, depth := range []string{"", "0", "-1", "4", "bad"} {
		t.Setenv("ZPGO_TEST_DEPTH", depth)
		if err := runHelper("scenario"); err == nil {
			t.Fatalf("accepted helper depth %q", depth)
		}
	}
}

func TestSpawnAndForkExecTrees(t *testing.T) {
	for _, size := range []int{40, 120} {
		for _, mode := range []string{"report", "nested-spawn", "nested-fork"} {
			for _, fork := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/fork=%v", size, mode, fork), func(t *testing.T) {
					p, r := testReporter(t, Options{Manual: true, RootName: "parent", NameSize: size})
					n := p.Root().Start("local task", 0)
					cmd := helperCommand(t, mode)
					cmd.Env = append(cmd.Env, fmt.Sprintf("ZPGO_NAME_SIZE=%d", size))
					if fork {
						pid, err := n.ForkExec(cmd.Path, cmd.Args, &syscall.ProcAttr{Env: cmd.Env, Files: []uintptr{0, 1, 2}})
						if err != nil {
							t.Fatal(err)
						}
						if err := waitPID(pid); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := n.Spawn(cmd); err != nil {
							t.Fatal(err)
						}
						if err := cmd.Wait(); err != nil {
							t.Fatal(err)
						}
					}
					got := flushRead(t, p, r)
					if got[1].name != "child" || got[1].parent != 0 {
						t.Fatal(got)
					}
					leaf := 2
					if mode != "report" {
						leaf = 3
						if got[2].name != "grandchild" || got[2].parent != 1 {
							t.Fatal(got)
						}
					}
					if len(got) != leaf+1 || got[leaf].name != "leaf" || int(got[leaf].parent) != leaf-1 || got[leaf].completed != 4 {
						t.Fatal(got)
					}
					if again := flushRead(t, p, r); !reflect.DeepEqual(again, got) {
						t.Fatal("tree lost after EOF")
					}
					if err := n.Spawn(helperCommand(t, "report")); !errors.Is(err, ErrProcessAttached) {
						t.Fatalf("duplicate: %v", err)
					}
					n.End()
					if got := flushRead(t, p, r); len(got) != 1 || got[0].completed != 1 {
						t.Fatal(got)
					}
				})
			}
		}
	}
}

func TestSpawnPreservesCommand(t *testing.T) {
	p, r := testReporter(t, Options{Manual: true})
	n := p.Root().Start("io", 0)
	cmd := helperCommand(t, "io")
	dir := t.TempDir()
	extra, err := os.CreateTemp(dir, "extra")
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, "ZPGO_DIR="+dir, "ZPGO_EXTRA_ENV=sentinel", "ZIG_PROGRESS=65535")
	cmd.ExtraFiles = make([]*os.File, 1, 4)
	cmd.ExtraFiles[0] = extra
	cmd.Stdin = strings.NewReader("input")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	env := append([]string(nil), cmd.Env...)
	if err := n.Spawn(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	if !reflect.DeepEqual(cmd.Env, env) || len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != extra {
		t.Fatal("command configuration changed")
	}
	if stdout.String() != "out:input" || stderr.String() != "err:sentinel" {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	_, _ = extra.Seek(0, 0)
	data, _ := io.ReadAll(extra)
	if string(data) != "extra preserved" {
		t.Fatalf("extra file: %q", data)
	}
	if got := flushRead(t, p, r); got[1].name != "child" {
		t.Fatal(got)
	}
}

func TestSpawnFailuresAndDisabledNodes(t *testing.T) {
	p, _ := testReporter(t, Options{Manual: true})
	n := p.Root().Start("worker", 0)
	if err := n.Spawn(nil); err == nil {
		t.Fatal("accepted nil cmd")
	}
	if err := n.Spawn(exec.Command(filepath.Join(t.TempDir(), "missing"))); err == nil {
		t.Fatal("started missing command")
	}
	if n.s.child != nil {
		t.Fatal("failed spawn attached child")
	}
	cmd := helperCommand(t, "report")
	if err := n.Spawn(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ForkExec("unused", nil, nil); !errors.Is(err, ErrProcessAttached) {
		t.Fatal(err)
	}
	n.End()
	for _, node := range []Node{{}, n} {
		cmd := helperCommand(t, "noenv")
		cmd.Env = append(cmd.Env, "ZIG_PROGRESS=3", "ZIG_PROGRESS=4")
		if err := node.Spawn(cmd); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		pid, err := node.ForkExec(cmd.Path, cmd.Args, &syscall.ProcAttr{Env: cmd.Env, Files: []uintptr{0, 1, 2}})
		if err != nil {
			t.Fatal(err)
		}
		if err := waitPID(pid); err != nil {
			t.Fatal(err)
		}
	}
	n = p.Root().Start("closed", 0)
	p.Close()
	cmd = helperCommand(t, "noenv")
	if err := n.Spawn(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestForkExecAttributesAndFailure(t *testing.T) {
	p, r := testReporter(t, Options{Manual: true})
	n := p.Root().Start("fork", 0)
	if _, err := n.ForkExec("/nonexistent-zpgo-test", nil, nil); err == nil {
		t.Fatal("fork exec succeeded unexpectedly")
	}
	if n.s.child != nil {
		t.Fatal("failed fork attached reader")
	}
	cmd := helperCommand(t, "report")
	attr := &syscall.ProcAttr{Env: cmd.Env, Files: make([]uintptr, 3, 8), Dir: t.TempDir(), Sys: &syscall.SysProcAttr{Setpgid: true}}
	copy(attr.Files, []uintptr{0, 1, 2})
	env, files := append([]string(nil), attr.Env...), append([]uintptr(nil), attr.Files...)
	pid, err := n.ForkExec(cmd.Path, cmd.Args, attr)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitPID(pid); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attr.Env, env) || !reflect.DeepEqual(attr.Files, files) || !attr.Sys.Setpgid {
		t.Fatal("attributes mutated")
	}
	if got := flushRead(t, p, r); got[1].name != "child" {
		t.Fatal(got)
	}
	// A nil Files slice must not turn the progress pipe into stdin.
	n = p.Root().Start("no standard streams", 0)
	pid, err = n.ForkExec(cmd.Path, cmd.Args, &syscall.ProcAttr{Env: cmd.Env})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitPID(pid); err != nil {
		t.Fatal(err)
	}
	if got := flushRead(t, p, r); got[2].name != "child" {
		t.Fatal(got)
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	pid, err = (Node{}).ForkExec(truePath, []string{truePath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitPID(pid); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorLimit(t *testing.T) {
	p, _ := testReporter(t, Options{Manual: true})
	n := p.Root().Start("limit", 0)
	cmd := helperCommand(t, "report")
	cmd.ExtraFiles = make([]*os.File, 65533)
	if err := n.Spawn(cmd); err == nil {
		t.Fatal("accepted oversized descriptor")
	}
	if _, err := n.ForkExec(cmd.Path, cmd.Args, &syscall.ProcAttr{Files: make([]uintptr, 65536)}); err == nil {
		t.Fatal("accepted oversized descriptor")
	}
	if n.s.child != nil {
		t.Fatal("failed setup attached child")
	}
}

func TestEndAndCloseDoNotWaitForChild(t *testing.T) {
	for _, closeReporter := range []bool{false, true} {
		p, _ := testReporter(t, Options{Manual: true})
		n := p.Root().Start("waiting", 0)
		cmd := helperCommand(t, "wait")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := n.Spawn(cmd); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = stdin.Close() })
		ready := make([]byte, 6)
		if _, err := io.ReadFull(stdout, ready); err != nil {
			t.Fatal(err)
		}
		if string(ready) != "ready\n" {
			t.Fatal(string(ready))
		}
		reader := n.s.child.pipe.file
		if closeReporter {
			p.Close()
		} else {
			n.End()
		}
		// Go 1.22 and newer Go releases wrap this closed-file error differently.
		if _, err := reader.Stat(); err == nil {
			t.Fatalf("reader not closed: %v", err)
		}
		if _, err := stdin.Write([]byte{1}); err != nil {
			t.Fatalf("process was terminated: %v", err)
		}
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpawnContextCancellation(t *testing.T) {
	p, _ := testReporter(t, Options{Manual: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessHelper$")
	cmd.Env = helperEnv("wait")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Root().Start("cancel", 0).Spawn(cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(stdout, make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancellation did not terminate process")
	}
}

func TestZpgCompatibility(t *testing.T) {
	zpg, err := exec.LookPath("zpg")
	if err != nil {
		if os.Getenv("ZPGO_REQUIRE_ZPG") != "" {
			t.Fatal("zpg is required but is not on PATH")
		}
		t.Skip("zpg is not on PATH; use the Nix development shell or set ZPGO_REQUIRE_ZPG=1 in CI")
	}
	for _, mode := range []string{"report", "scenario", "native", "capacity", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			logPath := filepath.Join(t.TempDir(), "progress.log")
			cmd := exec.CommandContext(ctx, zpg, "--test", "--test-output", logPath, os.Args[0], "-test.run=^TestProcessHelper$")
			cmd.Env = append(childEnv(helperEnv(mode), -1), "ZPGO_ZPG="+zpg)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			log, readErr := os.ReadFile(logPath)
			if err != nil || readErr != nil {
				t.Fatalf("zpg: %v, log: %v\n%s\n%s", err, readErr, stderr.String(), log)
			}
			text := string(log)
			for _, bad := range []string{"error:", "no progress received", "truncated", "incomplete"} {
				if strings.Contains(text, bad) {
					t.Fatalf("zpg rejected stream:\n%s", text)
				}
			}
			want := []string{"progress pipe opened", "progress pipe closed", "snapshot="}
			switch mode {
			case "report", "automatic":
				want = append(want, `node=0 parent=255 completed=6 total=10 name="child"`, `node=1 parent=0 completed=4 total=9 name="leaf"`)
			case "scenario":
				want = append(want, `node=1 parent=0 completed=0 total=0 name="spawned"`, `node=2 parent=0 completed=0 total=0 name="forked"`, `node=3 parent=1 completed=6 total=10 name="grandchild"`, `node=5 parent=2 completed=6 total=10 name="grandchild"`, `node=0 parent=255 completed=2 total=2 name="parent"`)
				if stdout.String() != "stdout unchanged\n" || stderr.String() != "stderr unchanged\n" {
					t.Fatalf("output changed: %q / %q", stdout.String(), stderr.String())
				}
			case "native":
				want = append(want, `node=1 parent=0 completed=3 total=8 name="native leaf"`)
			case "capacity":
				want = append(want, "nodes=253", `node=252 parent=0 completed=0 total=1 name="task-251"`, `node=1 parent=0 completed=1 total=2 name="replacement"`, `node=0 parent=255 completed=253 total=0 name="child"`)
			}
			for _, expected := range want {
				if !strings.Contains(text, expected) {
					t.Errorf("missing %q:\n%s", expected, text)
				}
			}
		})
	}
}
