package zpgo

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// A small kernel pipe forces short writes, even on hosts with large defaults.
func smallPipeReporter(t *testing.T) (*Progress, *os.File) {
	t.Helper()
	p, r := testReporter(t, Options{Manual: true, RootName: "original"})
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(p.out.fd), syscall.F_SETPIPE_SZ, 4096)
	if errno != 0 {
		t.Fatal(errno)
	}
	for i := 0; i < 60; i++ {
		p.Root().Start("task", 1)
	}
	return p, r
}

func TestPartialWrites(t *testing.T) {
	for _, changed := range []bool{false, true} {
		p, r := smallPipeReporter(t)
		if err := p.Flush(); !errors.Is(err, ErrWouldBlock) {
			t.Fatalf("first flush: %v", err)
		}
		if len(p.pending) == 0 {
			t.Fatal("test did not force a partial packet")
		}
		if err := p.Flush(); !errors.Is(err, ErrWouldBlock) {
			t.Fatalf("full pipe: %v", err)
		}
		if changed {
			p.Root().SetName("latest")
		}
		fd := int(r.Fd())
		if err := syscall.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
		var stream []byte
		drain := func() {
			var buf [4096]byte
			for {
				n, err := syscall.Read(fd, buf[:])
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
		for tries := 0; ; tries++ {
			drain()
			err := p.Flush()
			if err == nil {
				break
			}
			if !errors.Is(err, ErrWouldBlock) || tries > 10 {
				t.Fatalf("cannot finish packet: %v", err)
			}
		}
		drain()
		packetSize := 1 + 61*129
		wantPackets := 1
		if changed {
			wantPackets = 2
		}
		if len(stream) != packetSize*wantPackets {
			t.Fatalf("stream size %d, want %d", len(stream), packetSize*wantPackets)
		}
		for i := 0; i < wantPackets; i++ {
			records, ok := decode(stream[i*packetSize:(i+1)*packetSize], 120)
			want := "original"
			if i == 1 {
				want = "latest"
			}
			if !ok || len(records) != 61 || records[0].name != want {
				t.Fatal("partial writes damaged frame boundaries")
			}
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseWithPartialPacket(t *testing.T) {
	p, _ := smallPipeReporter(t)
	if err := p.Flush(); !errors.Is(err, ErrWouldBlock) {
		t.Fatal(err)
	}
	if err := p.Close(); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("Close: %v", err)
	}
	if p.Enabled() {
		t.Fatal("Close left output open")
	}
	if err := p.Close(); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("repeated Close: %v", err)
	}
}
