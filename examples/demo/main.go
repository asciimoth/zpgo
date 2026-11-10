// The demo reports local work and a child process under the same root.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/asciimoth/zpgo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	worker := len(os.Args) > 1 && os.Args[1] == "--worker"
	name, total := "demo", uint64(2)
	if worker {
		name, total = "worker", 20
	}
	p, err := zpgo.New(zpgo.Options{RootName: name, EstimatedTotalItems: total})
	if err != nil {
		return err
	}
	defer p.Close()
	if worker {
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			p.Root().CompleteOne()
		}
		return nil
	}
	path, err := os.Executable()
	if err != nil {
		return err
	}
	task := p.Root().Start("child", 0)
	defer task.End()
	cmd := exec.Command(path, "--worker")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := task.Spawn(cmd); err != nil {
		return err
	}
	local := p.Root().Start("local work", 10)
	for i := 0; i < 10; i++ {
		time.Sleep(50 * time.Millisecond)
		local.CompleteOne()
	}
	local.End()
	return cmd.Wait()
}
