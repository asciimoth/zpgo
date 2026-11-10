package zpgo

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestInvalidTokenEnvironment(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(^uintptr(0))}
	if _, err := defaultChildEnv(cmd); err == nil {
		t.Fatal("used the parent environment after a token environment error")
	}
	if err := (Node{}).Spawn(cmd); err == nil {
		_ = cmd.Wait()
		t.Fatal("started with an invalid token")
	}
	if cmd.Env != nil || cmd.Process != nil {
		t.Fatal("environment failure changed or started the command")
	}
}
