// SPDX-License-Identifier: AGPL-3.0-or-later

// Package agent implements the in-container Penguin agent: it supervises the
// game process and exposes console, logs, exit state, and stats to Wings over an
// authenticated HTTP/websocket API. The same package is imported by the
// penguin-agent binary (server side) and by the Wings LXC environment (client
// side) for the shared protocol types.
package agent

import "sync"

// LineBuffer is a bounded, thread-safe ring of the most recent output lines with
// fan-out to live subscribers. Appends never block on a slow subscriber — a full
// subscriber channel drops the line for that subscriber only.
type LineBuffer struct {
	mu      sync.Mutex
	lines   []string
	max     int
	subs    map[int]chan string
	nextSub int
}

// NewLineBuffer returns a buffer retaining up to max lines (min 1).
func NewLineBuffer(max int) *LineBuffer {
	if max < 1 {
		max = 1
	}
	return &LineBuffer{max: max, subs: make(map[int]chan string)}
}

// Append records a line, trims to the retention bound, and fans it out to live
// subscribers without blocking.
func (b *LineBuffer) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if over := len(b.lines) - b.max; over > 0 {
		b.lines = b.lines[over:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- line:
		default: // slow subscriber: drop for it only
		}
	}
}

// Lines returns up to the last n retained lines (all when n <= 0).
func (b *LineBuffer) Lines(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.lines) {
		n = len(b.lines)
	}
	out := make([]string, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// Subscribe registers a live line feed and returns its id and channel. The
// caller must Unsubscribe to release it.
func (b *LineBuffer) Subscribe() (int, <-chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSub
	b.nextSub++
	ch := make(chan string, 256)
	b.subs[id] = ch
	return id, ch
}

// Unsubscribe removes and closes a subscriber feed.
func (b *LineBuffer) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}
