//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package zpgo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testReporter(t *testing.T, opts Options) (*Progress, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	opts.File = w
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	// The reporter must retain its own descriptor.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, r
}

func readPacket(t *testing.T, r *os.File, nameSize int) []record {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var count [1]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 1+int(count[0])*(9+nameSize))
	packet[0] = count[0]
	if _, err := io.ReadFull(r, packet[1:]); err != nil {
		t.Fatal(err)
	}
	records, ok := decode(packet, nameSize)
	if !ok {
		t.Fatalf("invalid packet: %x", packet)
	}
	return records
}

func flushRead(t *testing.T, p *Progress, r *os.File) []record {
	t.Helper()
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	return readPacket(t, r, p.nameSize)
}

func TestTreeLifecycle(t *testing.T) {
	p, r := testReporter(t, Options{Manual: true, RootName: "build", EstimatedTotalItems: 2})
	a := p.Root().Start("assets", 10)
	b := p.Root().Start("code", 1)
	c := b.Start("compile", 4)
	a.Add(3)
	c.CompleteOne()
	got := flushRead(t, p, r)
	want := []record{{total: 2, name: "build", parent: 255}, {completed: 3, total: 10, name: "assets", parent: 0}, {total: 1, name: "code", parent: 0}, {completed: 1, total: 4, name: "compile", parent: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	a.End()
	a.End()
	a.CompleteOne()
	a.Start("ignored", 1).End()
	c.SetName("link")
	got = flushRead(t, p, r)
	if len(got) != 3 || got[0].completed != 1 || got[2].parent != 1 || got[2].name != "link" {
		t.Fatalf("after end: %#v", got)
	}
	b.End()
	c.End()
	c.CompleteOne()
	got = flushRead(t, p, r)
	if len(got) != 1 || got[0].completed != 2 {
		t.Fatalf("branch end: %#v", got)
	}
	replacement := p.Root().Start("replacement", 0)
	c.SetName("stale")
	if got := flushRead(t, p, r); got[1].name != "replacement" {
		t.Fatal(got)
	}
	replacement.End()
	p.Root().End()
	if p.Enabled() {
		t.Fatal("root End did not close reporter")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCountersAndCapacity(t *testing.T) {
	p, _ := testReporter(t, Options{Manual: true, EstimatedTotalItems: math.MaxUint64})
	root := p.Root()
	root.SetCompletedItems(math.MaxUint64)
	root.Add(math.MaxUint64)
	root.IncreaseEstimatedTotalItems(math.MaxUint64)
	if root.s.record.completed != math.MaxUint32 || root.s.record.total != math.MaxUint32-1 {
		t.Fatal(root.s.record)
	}
	root.SetCompletedItems(0)
	root.SetEstimatedTotalItems(0)
	root.IncreaseEstimatedTotalItems(8)
	root.Add(3)
	if root.s.record.completed != 3 || root.s.record.total != 8 {
		t.Fatal(root.s.record)
	}
	nodes := make([]Node, MaxNodes-1)
	for i := range nodes {
		nodes[i] = root.Start(fmt.Sprint(i), 0)
	}
	if extra := root.Start("overflow", 0); extra.p != nil {
		t.Fatal("capacity exceeded")
	}
	packet := p.snapshot()
	if int(packet[0]) != MaxNodes {
		t.Fatal("wrong capacity")
	}
	if _, ok := decode(packet, p.nameSize); !ok {
		t.Fatal("invalid full tree")
	}
	for _, n := range nodes {
		n.End()
	}
	if root.s.record.completed != 255 {
		t.Fatal(root.s.record)
	}
	if n := root.Start("reuse", 0); n.p == nil {
		t.Fatal("capacity not reusable")
	}
}

func TestDisabledAndOptions(t *testing.T) {
	for _, opts := range []Options{{NameSize: 41}, {RefreshInterval: -1}, {InitialDelay: -1}} {
		if _, err := New(opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
	for _, env := range []string{"", "bad", "65535"} {
		t.Setenv("ZIG_PROGRESS", env)
		p, err := New(Options{})
		if err != nil || p.Enabled() {
			t.Fatalf("env %q: %v", env, err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*Progress{{}, {nameSize: 120}} {
		n := p.Root().Start("task", 1)
		n.CompleteOne()
		n.Add(10)
		n.SetName("name")
		n.SetCompletedItems(3)
		n.SetEstimatedTotalItems(10)
		n.IncreaseEstimatedTotalItems(20)
		n.End()
		if p.Enabled() || p.Err() != nil || p.Flush() != nil || p.Close() != nil {
			t.Fatal("zero reporter is not disabled")
		}
	}
	file, err := os.CreateTemp(t.TempDir(), "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := New(Options{File: file}); err == nil {
		t.Fatal("accepted ordinary file")
	}
	t.Setenv("ZIG_PROGRESS", strconv.Itoa(int(file.Fd())))
	p, err := New(Options{})
	if err != nil || p.Enabled() {
		t.Fatal("accepted ordinary environment file")
	}
	if stat, _ := file.Stat(); stat.Size() != 0 {
		t.Fatal("wrote to ordinary file")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := New(Options{File: r}); err == nil {
		t.Fatal("accepted read-only pipe")
	}
	p, err = New(Options{File: w, Disable: true})
	if err != nil || p.Enabled() {
		t.Fatal("Disable ignored")
	}
	_ = w.Close()
	if _, err := New(Options{File: w}); err == nil {
		t.Fatal("accepted closed file")
	}
}

func TestEnvironmentChannel(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := int(w.Fd())
	t.Setenv("ZIG_PROGRESS", strconv.Itoa(fd))
	p, err := New(Options{Manual: true, RootName: "environment", NameSize: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.Enabled() || p.nameSize != 40 {
		t.Fatal("not enabled")
	}
	if got := flushRead(t, p, r); got[0].name != "environment" {
		t.Fatal(got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := syscall.Write(fd, []byte{0}); err != nil {
		t.Fatalf("caller descriptor closed: %v", err)
	}
}

func TestAutomaticAndClose(t *testing.T) {
	p, r := testReporter(t, Options{RootName: "automatic", InitialDelay: 10 * time.Millisecond, RefreshInterval: 5 * time.Millisecond})
	p.Root().CompleteOne()
	if got := readPacket(t, r, 120); got[0].completed != 1 {
		t.Fatal(got)
	}
	p.Root().CompleteOne()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	<-p.done
	var last []record
	for {
		var count [1]byte
		_, err := io.ReadFull(r, count[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		packet := make([]byte, 1+int(count[0])*129)
		packet[0] = count[0]
		if _, err := io.ReadFull(r, packet[1:]); err != nil {
			t.Fatal(err)
		}
		last, _ = decode(packet, 120)
	}
	if len(last) != 1 || last[0].completed != 2 {
		t.Fatal(last)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseBeforeInitialDelay(t *testing.T) {
	p, r := testReporter(t, Options{InitialDelay: time.Hour})
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readPacket(t, r, 120); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestBrokenPipe(t *testing.T) {
	p, r := testReporter(t, Options{Manual: true})
	r.Close()
	if err := p.Flush(); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("got %v", err)
	}
	if p.Enabled() || !errors.Is(p.Err(), syscall.EPIPE) {
		t.Fatal("reporter did not disconnect")
	}
	if !errors.Is(p.Close(), syscall.EPIPE) {
		t.Fatal("Close did not retain error")
	}
	p.Root().Start("disabled", 0).End()
}

func TestConcurrentUpdatesAndClose(t *testing.T) {
	p, _ := testReporter(t, Options{Manual: true})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				n := p.Root().Start("worker", 1)
				n.CompleteOne()
				n.SetName("done")
				n.End()
				p.Root().CompleteOne()
				_ = p.Enabled()
				_ = p.Err()
			}
		}()
	}
	wg.Wait()
	if got := p.Root().s.record.completed; got != 2400 {
		t.Fatalf("lost updates: %d", got)
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Root().Start("late", 0).End(); _ = p.Flush(); _ = p.Close() }()
	}
	wg.Wait()
}

func TestBackpressureKeepsPacketBoundaries(t *testing.T) {
	p, r := testReporter(t, Options{Manual: true})
	for i := 1; i < MaxNodes; i++ {
		p.Root().Start(fmt.Sprint(i), 0)
	}
	// Fill any pipe size, then change the tree while a packet may be partial.
	for i := 0; ; i++ {
		err := p.Flush()
		if errors.Is(err, ErrWouldBlock) {
			break
		}
		if err != nil || i > 1000 {
			t.Fatalf("cannot fill pipe: %v", err)
		}
	}
	p.Root().SetName("latest")
	rfd := int(r.Fd())
	if err := syscall.SetNonblock(rfd, true); err != nil {
		t.Fatal(err)
	}
	var stream []byte
	drain := func() {
		var buf [4096]byte
		for {
			n, err := syscall.Read(rfd, buf[:])
			if n > 0 {
				stream = append(stream, buf[:n]...)
			}
			if temporary(err) || n == 0 {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; ; i++ {
		drain()
		err := p.Flush()
		if err == nil {
			break
		}
		if !errors.Is(err, ErrWouldBlock) || i > 100 {
			t.Fatalf("cannot finish partial packet: %v", err)
		}
	}
	drain()
	var last []record
	for len(stream) > 0 {
		size := 1 + int(stream[0])*129
		if size > len(stream) {
			t.Fatal("truncated stream")
		}
		var ok bool
		last, ok = decode(stream[:size], 120)
		if !ok {
			t.Fatal("corrupt stream")
		}
		stream = stream[size:]
	}
	if len(last) != MaxNodes || last[0].name != "latest" {
		t.Fatal("latest tree not sent")
	}
}

func TestChildReadValidation(t *testing.T) {
	for _, size := range []int{40, 120} {
		r, w, err := progressPipe()
		if err != nil {
			t.Fatal(err)
		}
		c := &childPipe{pipe: r}
		defer c.close()
		defer w.Close()
		packet := encode([]record{{name: "child", parent: 255}, {name: "leaf", parent: 0}}, size)
		// Deliver every byte separately to exercise all possible packet splits.
		for i, b := range packet {
			if _, err := syscall.Write(int(w.Fd()), []byte{b}); err != nil {
				t.Fatal(err)
			}
			c.read(size)
			if i < len(packet)-1 && len(c.snapshot) != 0 {
				t.Fatal("accepted partial frame")
			}
		}
		if len(c.snapshot) != 2 {
			t.Fatal(c.snapshot)
		}
		bad := append(append([]byte(nil), packet...), 255)
		if _, err := syscall.Write(int(w.Fd()), bad); err != nil {
			t.Fatal(err)
		}
		c.read(size)
		if c.pipe != nil || len(c.snapshot) != 2 {
			t.Fatal("invalid child did not retain last tree")
		}
	}
}

func TestChildEOFAndInvalidTree(t *testing.T) {
	for _, tail := range [][]byte{{1, 0}, encode([]record{{parent: 255}, {parent: 1}}, 120), {0}} {
		r, w, err := progressPipe()
		if err != nil {
			t.Fatal(err)
		}
		c := &childPipe{pipe: r}
		good := encode([]record{{name: "last", parent: 255}}, 120)
		_, err = syscall.Write(int(w.Fd()), append(good, tail...))
		if err != nil {
			t.Fatal(err)
		}
		w.Close()
		c.read(120)
		if c.pipe != nil || len(c.pending) != 0 || len(c.snapshot) != 1 || c.snapshot[0].name != "last" {
			t.Fatal("lost last valid tree")
		}
	}
}

func TestSnapshotChildCapacityAndEmptyName(t *testing.T) {
	p, _ := testReporter(t, Options{Manual: true})
	a := p.Root().Start("local", 7)
	a.CompleteOne()
	a.s.child = &childPipe{snapshot: []record{{completed: 5, total: 10, parent: 255}, {name: "child", parent: 0}, {name: "grandchild", parent: 1}}}
	for i := 0; i < MaxNodes-3; i++ {
		p.Root().Start("other", 0)
	}
	got, ok := decode(p.snapshot(), 120)
	if !ok || len(got) != MaxNodes || got[1].name != "local" || got[1].completed != 5 || got[len(got)-1].name != "child" || got[len(got)-1].parent != 1 {
		t.Fatal(got)
	}
	a.End()
	if bytes.Contains(p.snapshot(), []byte("grandchild")) {
		t.Fatal("ended child tree retained")
	}
}
