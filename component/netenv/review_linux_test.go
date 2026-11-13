//go:build linux && !android

package netenv

import (
	"context"
	"strings"
	"testing"
)

func TestReviewK2MissingConfiguredInterfaceIsNone(t *testing.T) {
	// Linux interface names cannot be this long, so absence is deterministic.
	s, err := detectPlatform(context.Background(), strings.Repeat("x", 32))
	if err != nil || s.State != None {
		t.Fatalf("missing configured interface: snapshot=%+v err=%v", s, err)
	}
}
