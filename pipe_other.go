//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package zpgo

import "os"

func duplicateFile(*os.File) (*pipeFile, error)  { return nil, ErrUnsupported }
func duplicateFD(int, bool) (*pipeFile, error)   { return nil, ErrUnsupported }
func progressPipe() (*pipeFile, *os.File, error) { return nil, nil, ErrUnsupported }
func (*pipeFile) read([]byte) (int, error)       { return 0, ErrUnsupported }
func (*pipeFile) write([]byte) (int, error)      { return 0, ErrUnsupported }
func temporary(error) bool                       { return false }
