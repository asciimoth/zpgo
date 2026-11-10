// Package zpgo reports task trees through the Zig progress protocol.
//
// New reads the ZIG_PROGRESS environment variable. Without a usable channel,
// reporting is disabled. The package does not draw a terminal display or write
// to stdout or stderr. Use zpg or another supervisor to display the reports.
//
// Start tasks with Progress.Root and Node.Start. Defer Node.End for each task
// and Progress.Close for the reporter. Ending a task completes one item in its
// parent. Task methods are safe for concurrent use, and zero Nodes are no-ops.
//
// Node.Spawn starts an os/exec.Cmd with its own progress channel. The child's
// root replaces the attached task in reports. On supported Unix platforms,
// Node.ForkExec provides the same connection for syscall.ForkExec. It does not
// provide raw fork with continued execution of Go code.
//
// Names use 120 bytes by default, as in zppy and Zig 0.17. Set Options.NameSize
// to 40 for the published protocol. There is no format negotiation: the
// supervisor and every reporter in a process tree must use the same name size.
package zpgo
