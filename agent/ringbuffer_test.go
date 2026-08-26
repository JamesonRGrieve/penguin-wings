// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"reflect"
	"testing"
	"time"
)

func TestLineBufferTrimAndLines(t *testing.T) {
	t.Parallel()
	b := NewLineBuffer(3)
	for _, l := range []string{"a", "b", "c", "d", "e"} {
		b.Append(l)
	}
	if got, want := b.Lines(0), []string{"c", "d", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lines(0) = %v, want %v", got, want)
	}
	if got, want := b.Lines(2), []string{"d", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lines(2) = %v, want %v", got, want)
	}
	if got, want := b.Lines(10), []string{"c", "d", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Lines(10) = %v, want %v (capped)", got, want)
	}
}

func TestLineBufferSubscribe(t *testing.T) {
	t.Parallel()
	b := NewLineBuffer(10)
	id, ch := b.Subscribe()
	b.Append("hello")
	select {
	case got := <-ch:
		if got != "hello" {
			t.Errorf("subscriber got %q, want hello", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber received no line")
	}
	b.Unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Errorf("subscriber channel not closed after Unsubscribe")
	}
}
