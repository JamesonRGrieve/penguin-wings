// SPDX-License-Identifier: AGPL-3.0-or-later

package router

import (
	"errors"
	"strings"
	"testing"
)

func TestReadLimited(t *testing.T) {
	t.Parallel()
	if b, err := readLimited(strings.NewReader("abc"), 3); err != nil || string(b) != "abc" {
		t.Errorf("at limit = %q, %v", b, err)
	}
	if _, err := readLimited(strings.NewReader("abcd"), 3); !errors.Is(err, errLXCTooLarge) {
		t.Errorf("over limit err = %v, want errLXCTooLarge", err)
	}
}
