package zpgo

import (
	"encoding/binary"
	"strings"
	"unicode/utf8"
)

type record struct {
	completed, total uint32
	name             string
	parent           byte
}

func wireName(name string, size int) string {
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	if len(name) <= size {
		return strings.Clone(name)
	}
	if utf8.ValidString(name) {
		for !utf8.RuneStart(name[size]) {
			size--
		}
	}
	// Do not retain a large caller buffer for a short task label.
	return strings.Clone(name[:size])
}

func encode(records []record, nameSize int) []byte {
	size := 8 + nameSize
	packet := make([]byte, 1+len(records)*(size+1))
	packet[0] = byte(len(records))
	for i, r := range records {
		data := packet[1+i*size:]
		binary.LittleEndian.PutUint32(data, r.completed)
		binary.LittleEndian.PutUint32(data[4:], r.total)
		copy(data[8:8+nameSize], r.name)
		packet[1+len(records)*size+i] = r.parent
	}
	return packet
}

// decode validates and orders a tree. Forward parent references are valid.
func decode(packet []byte, nameSize int) ([]record, bool) {
	if len(packet) == 0 {
		return nil, false
	}
	count, size := int(packet[0]), 8+nameSize
	if count == 0 || count > MaxNodes || len(packet) != 1+count*(size+1) {
		return nil, false
	}
	parents := packet[1+count*size:]
	if parents[0] != 255 {
		return nil, false
	}
	children := make([][]int, count)
	for i := 1; i < count; i++ {
		parent := int(parents[i])
		if parent >= count {
			return nil, false
		}
		children[parent] = append(children[parent], i)
	}
	order := make([]int, 1, count)
	for i := 0; i < len(order); i++ {
		order = append(order, children[order[i]]...)
	}
	if len(order) != count {
		return nil, false
	}
	mapped := make([]byte, count)
	for i, old := range order {
		mapped[old] = byte(i)
	}
	records := make([]record, count)
	for i, old := range order {
		data := packet[1+old*size:][:size]
		total := binary.LittleEndian.Uint32(data[4:])
		if uint64(total) > maxTotal {
			return nil, false
		}
		parent := byte(255)
		if old != 0 {
			parent = mapped[parents[old]]
		}
		records[i] = record{binary.LittleEndian.Uint32(data), total, wireName(string(data[8:]), nameSize), parent}
	}
	return records, true
}

type childPipe struct {
	pipe     *pipeFile
	pending  []byte
	snapshot []record
}

func (c *childPipe) close() {
	if c.pipe != nil {
		_ = c.pipe.close()
		c.pipe = nil
	}
	c.pending = nil
}

func (c *childPipe) read(nameSize int) {
	var buf [4096]byte
	// A noisy child must not hold the reporter lock without a bound.
	for budget := 65536; c.pipe != nil && budget > 0; {
		n, err := c.pipe.read(buf[:])
		if n > 0 {
			budget -= n
			c.pending = append(c.pending, buf[:n]...)
			for len(c.pending) > 0 {
				count := int(c.pending[0])
				if count == 0 || count > MaxNodes {
					c.close()
					return
				}
				size := 1 + count*(9+nameSize)
				if len(c.pending) < size {
					break
				}
				snapshot, ok := decode(c.pending[:size], nameSize)
				if !ok {
					c.close()
					return
				}
				c.snapshot = snapshot
				c.pending = c.pending[size:]
			}
		}
		if err != nil && temporary(err) {
			return
		}
		if err != nil || n == 0 {
			c.close()
			return
		}
	}
}

func (p *Progress) snapshot() []byte {
	records := make([]record, len(p.nodes), MaxNodes)
	indices := make(map[*nodeState]byte, len(p.nodes))
	for i, s := range p.nodes {
		indices[s] = byte(i)
		r := s.record
		r.parent = 255
		if s.parent != nil {
			r.parent = indices[s.parent]
		}
		records[i] = r
	}
	// Local nodes have priority. Child trees use the remaining capacity.
	for i, s := range p.nodes {
		c := s.child
		if c == nil {
			continue
		}
		c.read(p.nameSize)
		if len(c.snapshot) == 0 {
			continue
		}
		root := c.snapshot[0]
		root.parent = records[i].parent
		if root.name == "" {
			root.name = records[i].name
		}
		records[i] = root
		mapped := make([]byte, len(c.snapshot))
		mapped[0] = byte(i)
		for j, r := range c.snapshot[1:] {
			if len(records) == MaxNodes {
				break
			}
			mapped[j+1] = byte(len(records))
			r.parent = mapped[r.parent]
			records = append(records, r)
		}
	}
	return encode(records, p.nameSize)
}
