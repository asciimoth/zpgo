package zpgo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxNodes is the maximum number of nodes in a report, including the root.
	MaxNodes = 253
	// DefaultNameSize matches zppy and Zig 0.17.
	DefaultNameSize = 120
	// LegacyNameSize matches the published protocol and older Zig releases.
	LegacyNameSize = 40
	maxCompleted   = uint64(1<<32 - 1)
	maxTotal       = maxCompleted - 1
)

var (
	// ErrWouldBlock means that a report could not yet be sent in full.
	ErrWouldBlock = errors.New("zpgo: progress pipe is full")
	// ErrProcessAttached means that the node already has a child process.
	ErrProcessAttached = errors.New("zpgo: a process is already attached to this node")
	// ErrUnsupported means that pipe reporting is not supported on this platform.
	ErrUnsupported = errors.New("zpgo: pipe reporting is not supported on this platform")
)

// Options configures a reporter. The zero value reads ZIG_PROGRESS and sends
// reports every 80 milliseconds, starting immediately.
type Options struct {
	RootName            string
	EstimatedTotalItems uint64
	// File overrides ZIG_PROGRESS. It must be a writable pipe. New duplicates
	// it; the caller retains ownership. The shared pipe becomes nonblocking.
	File    *os.File
	Disable bool
	// Manual disables the update goroutine. Call Flush to collect and send reports.
	Manual          bool
	RefreshInterval time.Duration
	InitialDelay    time.Duration
	// NameSize is 120 (the default) or 40. Every process must use the same size.
	NameSize int
}

// Progress reports a task tree. Its methods and its nodes are safe for concurrent
// use. Do not copy a Progress. Use one reporter per pipe, and always close it.
// The zero value is a disabled reporter.
type Progress struct {
	mu              sync.Mutex
	root            Node
	nodes           []*nodeState
	out             *pipeFile
	nameSize        int
	closed          bool
	err             error
	closeErr        error
	stop            chan struct{}
	done            chan struct{}
	pending         []byte
	pendingSnapshot []byte
	lastSnapshot    []byte
}

// New creates a reporter. A missing or unusable environment channel disables
// reporting without an error. Invalid options or an unusable explicit File
// return an error. Reporting never writes to stdout or stderr.
func New(opts Options) (*Progress, error) {
	if opts.NameSize == 0 {
		opts.NameSize = DefaultNameSize
	}
	if opts.NameSize != DefaultNameSize && opts.NameSize != LegacyNameSize {
		return nil, fmt.Errorf("zpgo: name size must be 40 or 120")
	}
	if opts.RefreshInterval < 0 || opts.InitialDelay < 0 {
		return nil, fmt.Errorf("zpgo: timer durations must not be negative")
	}
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = 80 * time.Millisecond
	}
	p := &Progress{nameSize: opts.NameSize}
	if !opts.Disable {
		var err error
		if opts.File != nil {
			p.out, err = duplicateFile(opts.File)
			if err != nil {
				return nil, fmt.Errorf("zpgo: progress file: %w", err)
			}
		} else if fd, ok := environmentFD(os.Getenv("ZIG_PROGRESS")); ok {
			p.out, _ = duplicateFD(fd, true)
		}
	}
	if p.out == nil {
		return p, nil
	}
	s := &nodeState{active: true, record: record{name: wireName(opts.RootName, p.nameSize), total: saturate(opts.EstimatedTotalItems, maxTotal)}}
	p.nodes = []*nodeState{s}
	p.root = Node{p: p, s: s}
	if !opts.Manual {
		p.stop, p.done = make(chan struct{}), make(chan struct{})
		go p.run(p.stop, p.done, opts.InitialDelay, opts.RefreshInterval)
	}
	return p, nil
}

func environmentFD(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(value, 10, 16)
	return int(n), err == nil
}

// Root returns the root task. A disabled reporter returns a no-op node.
func (p *Progress) Root() Node { return p.root }

// Enabled reports whether the progress channel is open.
func (p *Progress) Enabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out != nil
}

// Err returns the first permanent output error, if any. Such an error disables
// the reporter. A full pipe is temporary and does not set Err.
func (p *Progress) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Flush collects child reports and tries to send the current tree without
// waiting for pipe space. ErrWouldBlock asks the caller to retry later. Partial
// packets finish before newer packets. Disabled or closed reporters do nothing.
func (p *Progress) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return p.err
	}
	return p.flushLocked(false)
}

func (p *Progress) flushLocked(final bool) error {
	snapshot := p.snapshot()
	if len(p.pending) != 0 {
		old := p.pendingSnapshot
		if err := p.write(p.pending); err != nil {
			return err
		}
		if bytes.Equal(old, snapshot) {
			return nil
		}
	}
	if final && bytes.Equal(p.lastSnapshot, snapshot) {
		return nil
	}
	return p.write(snapshot)
}

func (p *Progress) write(packet []byte) error {
	n, err := p.out.write(packet)
	if n > 0 {
		if len(p.pending) == 0 {
			p.pendingSnapshot = packet
		}
		p.pending = packet[n:]
		if len(p.pending) == 0 {
			p.lastSnapshot = p.pendingSnapshot
			p.pendingSnapshot = nil
		}
	}
	if err != nil && !temporary(err) {
		p.err = fmt.Errorf("zpgo: write progress: %w", err)
		p.disconnect()
		return p.err
	}
	if n != len(packet) {
		return ErrWouldBlock
	}
	return nil
}

func (p *Progress) run(stop <-chan struct{}, done chan<- struct{}, delay, interval time.Duration) {
	defer close(done)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			_ = p.Flush()
			timer.Reset(interval)
		}
	}
}

// Close stops automatic reports, tries to send the final tree, and closes owned
// descriptors. It does not wait for or terminate child processes. Final delivery
// is best effort: ErrWouldBlock means the receiver must drain the pipe sooner.
// Repeated calls return the same result. Ending the root also calls Close.
func (p *Progress) Close() error {
	p.mu.Lock()
	if !p.closed {
		p.closeErr = p.err
		if p.out != nil {
			p.closeErr = p.flushLocked(true)
		}
		p.closed = true
		p.disconnect()
	}
	err, done := p.closeErr, p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
	return err
}

func (p *Progress) disconnect() {
	if p.out != nil {
		_ = p.out.close()
		p.out = nil
	}
	for _, s := range p.nodes {
		s.active = false
		if s.child != nil {
			s.child.close()
			s.child = nil
		}
	}
	p.nodes = nil
	p.pending, p.pendingSnapshot, p.lastSnapshot = nil, nil, nil
	if p.stop != nil {
		close(p.stop)
		p.stop = nil
	}
}

func saturate(n, limit uint64) uint32 {
	if n > limit {
		n = limit
	}
	return uint32(n)
}
