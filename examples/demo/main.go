//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// The demo combines a native subprocess and a forked Go worker.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/asciimoth/zpgo"
)

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "--worker" {
		err = runWorker()
	} else {
		err = runDemo()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDemo() error {
	p, err := zpgo.New(zpgo.Options{RootName: "GO demo", EstimatedTotalItems: 2})
	if err != nil {
		return err
	}
	defer p.Close()

	path, err := os.Executable()
	if err != nil {
		return err
	}
	nativeTask := p.Root().Start("GO zpg --mock", 0)
	cmd := exec.Command("zpg", "--mock")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := nativeTask.Spawn(cmd); err != nil {
		return err
	}

	workerTask := p.Root().Start("GO worker", 0)
	pid, err := workerTask.ForkExec(path, []string{path, "--worker"}, &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	// Wait for both processes. Each task ends when its process exits.
	nativeDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = p.Flush()
		nativeTask.End()
		nativeDone <- err
	}()
	workerErr := waitWorker(pid)
	_ = p.Flush()
	workerTask.End()
	nativeErr := <-nativeDone
	if nativeErr != nil {
		nativeErr = fmt.Errorf("zpg --mock: %w", nativeErr)
	}
	return errors.Join(nativeErr, workerErr)
}

func waitWorker(pid int) error {
	var status syscall.WaitStatus
	for {
		_, err := syscall.Wait4(pid, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("GO worker: wait: %w", err)
		}
		if !status.Exited() || status.ExitStatus() != 0 {
			return fmt.Errorf("GO worker: %v", status)
		}
		return nil
	}
}

func runWorker() error {
	p, err := zpgo.New(zpgo.Options{RootName: "GO worker", EstimatedTotalItems: 3})
	if err != nil {
		return err
	}
	defer p.Close()

	discover := p.Root().Start("GO discover", 2)
	step(discover, "GO read config")
	step(discover, "GO scan inputs")
	discover.End()

	build := p.Root().Start("GO build", 2)
	assets := build.Start("GO assets", 2)
	step(assets, "GO decode")
	step(assets, "GO compress")
	assets.End()
	code := build.Start("GO code", 2)
	step(code, "GO compile")
	step(code, "GO link")
	code.End()
	build.End()

	publish := p.Root().Start("GO publish", 2)
	step(publish, "GO upload")
	step(publish, "GO write manifest")
	publish.End()
	fmt.Fprintln(os.Stderr, "GO worker: complete")
	return nil
}

func step(parent zpgo.Node, name string) {
	task := parent.Start(name, 1)
	fmt.Println("GO worker:", name)
	time.Sleep(200 * time.Millisecond)
	task.CompleteOne()
	task.End()
}
