package zpgo_test

import (
	"fmt"
	"os/exec"

	"github.com/asciimoth/zpgo"
)

func Example() {
	p, err := zpgo.New(zpgo.Options{RootName: "build", EstimatedTotalItems: 1})
	if err != nil {
		panic(err)
	}
	defer p.Close()
	task := p.Root().Start("assets", 3)
	defer task.End()
	for i := 0; i < 3; i++ {
		task.CompleteOne()
	}
	// No Output directive: reports go to the supervisor, if present.
}

func ExampleNode_Spawn() {
	p, err := zpgo.New(zpgo.Options{RootName: "build"})
	if err != nil {
		panic(err)
	}
	defer p.Close()
	task := p.Root().Start("compile", 0)
	defer task.End()
	cmd := exec.Command("worker", "--compile")
	if err := task.Spawn(cmd); err != nil {
		fmt.Println(err)
		return
	}
	if err := cmd.Wait(); err != nil {
		fmt.Println(err)
	}
}
