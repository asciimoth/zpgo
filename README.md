# zpgo

`zpgo` is a Go reporter for the Zig progress protocol. It sends tasks,
subtasks, and child process progress to a supervisor such as
[`zpg`](https://github.com/asciimoth/zpg).

Requires Go 1.22 or later. There are no external Go dependencies.
Pipe reporting supports Linux, macOS, FreeBSD, OpenBSD, NetBSD, and DragonFly BSD.
On other platforms, environment reporting is disabled; an explicit `File`
returns `ErrUnsupported`.

## Install

```sh
go get github.com/asciimoth/zpgo
```

## Report tasks

```go
p, err := zpgo.New(zpgo.Options{
    RootName:            "build",
    EstimatedTotalItems: 2,
})
if err != nil {
    return err
}
defer p.Close()

for _, name := range []string{"assets", "code"} {
    task := p.Root().Start(name, 10)
    for i := 0; i < 10; i++ {
        // Do one item of work.
        task.CompleteOne()
    }
    task.End()
}
```

Run a program with a progress display:

```sh
go build -o /tmp/zpgo-demo ./examples/demo
zpg /tmp/zpgo-demo
```

Without a usable `ZIG_PROGRESS` channel, task operations have no effect.
The library does not print to stdout or stderr and does not draw a display.

`End` removes the task and its descendants, then completes one item in its
parent. Do not also call `CompleteOne` for the same task. Repeated `End` calls
have no effect. Ending the root closes the reporter. These operations do not
indicate success or failure.

## Child processes

Configure an `exec.Cmd` normally. Call `Spawn` in place of `cmd.Start`, then
call `cmd.Wait`:

```go
task := p.Root().Start("compile", 0)
defer task.End()

cmd := exec.CommandContext(ctx, "worker", "--input", "source")
cmd.Stdout = os.Stdout
cmd.Stderr = os.Stderr
if err := task.Spawn(cmd); err != nil {
    return err
}
return cmd.Wait()
```

Each process gets a separate nonblocking pipe. Its root replaces the task in
reports; its descendants appear below that task. An empty child root name
keeps the local task name. Before the first complete report, the local task
state remains visible. After EOF or process exit, the last complete child tree
remains until you end the task. Invalid child reports close that child's pipe
and keep its last valid tree.

Use one process per active node. A second attachment returns
`ErrProcessAttached`, even after the first process exits. A failed start leaves
the node available. Ending a task or closing a reporter closes its child
progress pipes; it does not wait for or terminate the processes.

`Spawn` preserves the command's standard streams, context, directory, and
existing `ExtraFiles`. It copies `Env` and `ExtraFiles` for startup and restores
both fields before return. It sets `ZIG_PROGRESS` only in the child environment.
Disabled, ended, and closed nodes still start processes, with `ZIG_PROGRESS`
removed. Do not explicitly pass the upstream descriptor to children.

### Fork and exec

On the supported Unix platforms, `Node.ForkExec` has the same signature as
`syscall.ForkExec`. It copies the process attributes and adds a private progress
pipe. It returns a PID; the caller must reap the child, then end its task.

```go
pid, err := task.ForkExec(path, []string{path, "--worker"}, &syscall.ProcAttr{
    Env:   os.Environ(),
    Files: []uintptr{0, 1, 2},
})
if err != nil {
    return err
}
var status syscall.WaitStatus
for {
    _, err = syscall.Wait4(pid, &status, 0, nil)
    if err != syscall.EINTR {
        break
    }
}
// Check err and status before using the worker's result.
```

As in `syscall.ForkExec`, there is no PATH lookup. A nil environment is empty;
omitted file descriptors are closed. Unlike `exec.Cmd`, `syscall.ForkExec` does
not remove duplicate environment keys. Supply each key once.

The Go runtime does not support a raw `fork` followed by normal Go execution.
To run a Go worker, fork and exec the current executable with a worker argument,
then create a new reporter there. Nested `Spawn` and `ForkExec` calls combine
their progress trees. The parent's reporter and environment stay unchanged.

## Configuration and limits

| Option | Meaning |
| --- | --- |
| `RootName` | Root task label. |
| `EstimatedTotalItems` | Root total. Zero means unknown. |
| `File` | Explicit writable pipe; otherwise read `ZIG_PROGRESS`. |
| `Disable` | Disable reporting. |
| `Manual` | Send and collect reports only when `Flush` is called. |
| `RefreshInterval` | Time between reports; zero uses 80 ms. |
| `InitialDelay` | Delay before the first report; zero starts immediately. |
| `NameSize` | 120 by default; use 40 for the published format. |

The published [protocol specification][spec] uses 40-byte names.
[`zppy`][zppy] and Zig 0.17 use 120-byte names. The protocol does not negotiate
this size. Set it to match your supervisor and all child reporters.

Names stop at the first NUL and are truncated to the configured byte size.
Valid UTF-8 names are not split within a character. Go strings can also carry
raw name bytes. Completed counts saturate at `2^32-1`; totals saturate at
`2^32-2`, because Zig reserves the next value for internal use.

A report holds at most 253 nodes, including the root. `Start` returns a no-op
node when local capacity is full. Local tasks have priority over imported
child descendants. Ending tasks releases capacity, and old handles stay inert.

All task and reporter methods are safe for concurrent use. Do not copy a
`Progress`; copied `Node` values refer to the same task. Zero values are safe
and disabled. As with the standard library, do not modify an `exec.Cmd` or its
attributes concurrently with startup.

Use one reporter per output pipe and always close it. `New` duplicates an
explicit `File` and leaves ownership with the caller. It sets the pipe to
nonblocking mode, which also affects other copies of that descriptor. An
inherited environment descriptor is marked close-on-exec; it is not closed.

`Flush` does not wait for pipe space. It returns `ErrWouldBlock` if the current
tree could not be sent in full. Retry later; partial packets finish before
newer ones. A permanent write error disables reporting and is available through
`Err`. `Close` stops automatic updates and attempts a final report. It returns
`ErrWouldBlock` if the pipe is still full; final delivery is not guaranteed.
Neither method sets completed counts to their totals.

Nodes provide `Start`, `End`, `CompleteOne`, `Add`, `SetCompletedItems`,
`SetEstimatedTotalItems`, `IncreaseEstimatedTotalItems`, and `SetName`.
Use `go doc` for the full API.

## Tests

```sh
just test
just test-race
just coverage
just test-compat
```

The tests cover both wire formats, task lifetimes, counter limits, concurrent
updates, full pipes, partial messages, invalid child trees, subprocess I/O,
descriptor handling, cancellation, and nested spawn/fork-and-exec trees.
Fuzz targets cover packet decoding and name truncation.

Compatibility tests use `zpg --test` as an independent packet validator and
`zpg --report` as a native child. They run when `zpg` is on PATH.
`just test-compat` requires it and fails if it is missing. The Nix development
shell and Nix Go checks include `zpg`.

Test helpers reject more than three child generations. On Linux with a systemd
user session, use `just test-limited -race` to cap the whole test process tree
at 768 MiB and 96 tasks, with no swap use.

The implementation was checked against Zig's [`std.Progress`][zig-progress]
and [`zppy`][zppy]. The package uses the CC0-1.0 license.

[spec]: https://andrewkelley.me/post/zig-new-cli-progress-bar-explained.html#the-zig-progress-protocol-specification
[zppy]: https://github.com/asciimoth/zppy
[zig-progress]: https://github.com/ziglang/zig/blob/master/lib/std/Progress.zig
