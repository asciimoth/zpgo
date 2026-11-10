package zpgo

import (
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"testing"
)

func TestChildEnvironment(t *testing.T) {
	env := []string{"KEEP=value", "ZIG_PROGRESS=3", "zig_progress=4", "ZiG_PrOgReSs=5", "ZIG_PROGRESS_EXTRA=6", "ZIG_PROGRESS"}
	original := append([]string(nil), env...)
	for _, fold := range []bool{false, true} {
		want := []string{"KEEP=value"}
		if !fold {
			want = append(want, "zig_progress=4", "ZiG_PrOgReSs=5")
		}
		want = append(want, "ZIG_PROGRESS_EXTRA=6")
		if got := childEnvCase(env, -1, fold); !reflect.DeepEqual(got, want) {
			t.Fatalf("disabled, fold=%v: %q, want %q", fold, got, want)
		}
		want = append(want, "ZIG_PROGRESS=7")
		if got := childEnvCase(env, 7, fold); !reflect.DeepEqual(got, want) {
			t.Fatalf("enabled, fold=%v: %q, want %q", fold, got, want)
		}
	}
	if !reflect.DeepEqual(env, original) {
		t.Fatal("changed caller environment")
	}
}

func TestSpawnRejectsInvalidEnvironment(t *testing.T) {
	if runtime.GOOS == "plan9" {
		t.Skip("Plan 9 permits NUL in environment values")
	}
	for _, entry := range []string{"KEEP=bad\x00value", "ZIG_PROGRESS=3\x00"} {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), entry)
		original := append([]string(nil), cmd.Env...)
		err := (Node{}).Spawn(cmd)
		if cmd.Process != nil {
			_ = cmd.Wait()
			t.Fatal("started a process with an invalid environment")
		}
		if err == nil {
			t.Fatal("accepted NUL in environment")
		}
		if !reflect.DeepEqual(cmd.Env, original) {
			t.Fatal("changed command environment after failure")
		}
	}
}
