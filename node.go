package zpgo

type nodeState struct {
	parent *nodeState
	record record
	active bool
	child  *childPipe
}

// Node is a task handle. Copies refer to the same task. The zero value and ended
// nodes are safe no-ops. End removes the whole branch and completes one item in
// the parent. Process exit alone does not end a node.
type Node struct {
	p *Progress
	s *nodeState
}

// Start creates a child task. A zero total means unknown. At capacity it returns
// a no-op node. Counts saturate at the protocol limits.
func (n Node) Start(name string, estimatedTotalItems uint64) Node {
	if n.p == nil {
		return Node{}
	}
	p := n.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if !n.s.active || len(p.nodes) == MaxNodes {
		return Node{}
	}
	s := &nodeState{active: true, parent: n.s, record: record{name: wireName(name, p.nameSize), total: saturate(estimatedTotalItems, maxTotal)}}
	p.nodes = append(p.nodes, s)
	return Node{p: p, s: s}
}

func (n Node) update(f func(*record)) {
	if n.p == nil {
		return
	}
	n.p.mu.Lock()
	defer n.p.mu.Unlock()
	if n.s.active {
		f(&n.s.record)
	}
}

// CompleteOne adds one completed item.
func (n Node) CompleteOne() { n.Add(1) }

// Add adds completed items, saturating at 2^32-1.
func (n Node) Add(count uint64) {
	n.update(func(r *record) { r.completed += saturate(count, maxCompleted-uint64(r.completed)) })
}

// SetCompletedItems sets the completed count, saturating at 2^32-1.
func (n Node) SetCompletedItems(count uint64) {
	n.update(func(r *record) { r.completed = saturate(count, maxCompleted) })
}

// SetEstimatedTotalItems sets the total, saturating at 2^32-2. Zero means unknown.
func (n Node) SetEstimatedTotalItems(count uint64) {
	n.update(func(r *record) { r.total = saturate(count, maxTotal) })
}

// IncreaseEstimatedTotalItems adds to the total, saturating at 2^32-2.
func (n Node) IncreaseEstimatedTotalItems(count uint64) {
	n.update(func(r *record) { r.total += saturate(count, maxTotal-uint64(r.total)) })
}

// SetName changes the label. Names stop at the first NUL and are truncated at
// NameSize bytes. Valid UTF-8 names are never split inside a character.
func (n Node) SetName(name string) {
	n.update(func(r *record) { r.name = wireName(name, n.p.nameSize) })
}

// End removes this task and its descendants. Repeated calls have no effect.
// Ending the root closes the reporter; use Progress.Close to check its error.
func (n Node) End() {
	if n.p == nil {
		return
	}
	p := n.p
	if n.s == p.root.s {
		_ = p.Close()
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !n.s.active {
		return
	}
	n.s.active = false
	n.s.parent.record.completed += saturate(1, maxCompleted-uint64(n.s.parent.record.completed))
	kept := p.nodes[:0]
	for _, s := range p.nodes {
		if s.parent != nil && !s.parent.active {
			s.active = false
		}
		if s.active {
			kept = append(kept, s)
		} else if s.child != nil {
			s.child.close()
			s.child = nil
		}
	}
	clear(p.nodes[len(kept):])
	p.nodes = kept
}
