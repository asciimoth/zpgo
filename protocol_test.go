package zpgo

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWireFormat(t *testing.T) {
	for _, size := range []int{40, 120} {
		records := []record{{completed: 0x12345678, total: 100, name: "root", parent: 255}, {completed: 3, total: 7, name: "child", parent: 0}}
		packet := encode(records, size)
		want := make([]byte, 1+2*(9+size))
		want[0] = 2
		copy(want[1:], []byte{0x78, 0x56, 0x34, 0x12, 100, 0, 0, 0})
		copy(want[9:], "root")
		copy(want[9+size:], []byte{3, 0, 0, 0, 7, 0, 0, 0})
		copy(want[17+size:], "child")
		want[len(want)-2] = 255
		if !bytes.Equal(packet, want) {
			t.Fatalf("size %d: wire mismatch\ngot %x\nwant %x", size, packet, want)
		}
		got, ok := decode(want, size)
		if !ok || !reflect.DeepEqual(got, records) {
			t.Fatalf("decode: %v %v", got, ok)
		}
	}
}

func TestDecodeTrees(t *testing.T) {
	valid := []record{{name: "root", parent: 255}, {name: "leaf", parent: 2}, {name: "branch", parent: 0}}
	got, ok := decode(encode(valid, 40), 40)
	if !ok || got[1].name != "branch" || got[2].parent != 1 {
		t.Fatalf("forward references: %v %v", got, ok)
	}
	for _, parents := range [][]byte{{0}, {255, 1}, {255, 2, 1}, {255, 255}, {255, 254}, {255, 4}, {255, 2, 3, 1}} {
		records := make([]record, len(parents))
		for i, parent := range parents {
			records[i].parent = parent
		}
		if _, ok := decode(encode(records, 40), 40); ok {
			t.Fatalf("accepted parents %v", parents)
		}
	}
	packet := encode(valid, 40)
	for i := 0; i < len(packet); i++ {
		if _, ok := decode(packet[:i], 40); ok {
			t.Fatalf("accepted truncation at %d", i)
		}
	}
	for _, packet := range [][]byte{{0}, {254}, {255}, append(encode(valid, 40), 0)} {
		if _, ok := decode(packet, 40); ok {
			t.Fatalf("accepted invalid length: %x", packet)
		}
	}
	packet = encode(valid, 40)
	binary.LittleEndian.PutUint32(packet[5:], math.MaxUint32)
	if _, ok := decode(packet, 40); ok {
		t.Fatal("accepted reserved total")
	}
}

func TestNames(t *testing.T) {
	for _, size := range []int{40, 120} {
		for _, tc := range []struct{ input, want string }{
			{"", ""}, {"abc\x00def", "abc"}, {strings.Repeat("a", size+1), strings.Repeat("a", size)},
			{strings.Repeat("a", size-1) + "é", strings.Repeat("a", size-1)},
			{strings.Repeat("界", size), strings.Repeat("界", size/3)},
			{string([]byte{0xff, 0xfe}), string([]byte{0xff, 0xfe})},
		} {
			if got := wireName(tc.input, size); got != tc.want {
				t.Errorf("wireName(%q, %d) = %q, want %q", tc.input, size, got, tc.want)
			}
		}
	}
}

func TestEnvironmentFD(t *testing.T) {
	for _, value := range []string{"", "-1", "+3", " 3", "3 ", "0x03", "65536", "99999999999999999", "٣", "3\n"} {
		if _, ok := environmentFD(value); ok {
			t.Errorf("accepted %q", value)
		}
	}
	for value, want := range map[string]int{"0": 0, "3": 3, "0003": 3, "65535": 65535, strings.Repeat("0", 10000) + "42": 42} {
		if fd, ok := environmentFD(value); !ok || fd != want {
			t.Errorf("%q: %d, %v", value, fd, ok)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(encode([]record{{name: "root", parent: 255}, {name: "child", parent: 0}}, 40), false)
	f.Add(encode([]record{{name: "root", parent: 255}}, 120), true)
	f.Add([]byte{255}, true)
	f.Fuzz(func(t *testing.T, packet []byte, modern bool) {
		size := 40
		if modern {
			size = 120
		}
		records, ok := decode(packet, size)
		if !ok {
			return
		}
		if len(records) == 0 || len(records) > MaxNodes || records[0].parent != 255 {
			t.Fatal("invalid decoded tree")
		}
		for i, r := range records {
			if i > 0 && int(r.parent) >= i {
				t.Fatal("parent does not precede child")
			}
		}
		again, ok := decode(encode(records, size), size)
		if !ok || !reflect.DeepEqual(records, again) {
			t.Fatal("round trip failed")
		}
	})
}

func FuzzName(f *testing.F) {
	f.Add("hello界", uint8(40))
	f.Fuzz(func(t *testing.T, name string, limit uint8) {
		got := wireName(name, int(limit))
		if len(got) > int(limit) || strings.ContainsRune(got, 0) {
			t.Fatal("invalid name length or NUL")
		}
		if utf8.ValidString(name) && !utf8.ValidString(got) {
			t.Fatal("split UTF-8")
		}
	})
}
