//go:build !linux && !darwin && !netbsd

package cliprovider

import (
	"strings"
	"testing"
)

func TestOpenFileNoFollowFailsClosedOnUnsupportedPlatform(t *testing.T) {
	file, err := openFileNoFollow(t.TempDir(), "image", nil)
	if file != nil {
		_ = file.Close()
		t.Fatal("openFileNoFollow returned a file on an unsupported platform")
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported on this platform") {
		t.Fatalf("openFileNoFollow() error = %v, want explicit unsupported-platform error", err)
	}
}
