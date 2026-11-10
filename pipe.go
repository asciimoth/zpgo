package zpgo

import "os"

// Use raw nonblocking syscalls: os.File.Read/Write may wait through Go's poller.
type pipeFile struct {
	file *os.File
	fd   int
}

func (p *pipeFile) close() error { return p.file.Close() }
