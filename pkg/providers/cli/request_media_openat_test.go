//go:build linux || darwin || netbsd

package cliprovider

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenFileNoFollowRejectsTerminalSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "wanted")
	unrelated := filepath.Join(root, "unrelated")
	if err := os.WriteFile(wanted, []byte("wanted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("unrelated bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	input, err := openFileNoFollow(root, "wanted", func(component string) {
		if called || component != "wanted" {
			return
		}
		called = true
		if renameErr := os.Rename(wanted, wanted+".old"); renameErr != nil {
			t.Fatal(renameErr)
		}
		if symlinkErr := os.Symlink("unrelated", wanted); symlinkErr != nil {
			t.Fatal(symlinkErr)
		}
	})
	if input != nil {
		_ = input.Close()
		t.Fatal("open succeeded after terminal component was swapped to a symlink")
	}
	if !called || err == nil || !strings.Contains(err.Error(), "non-symlink path component") {
		t.Fatalf("openFileNoFollow() called=%v error=%v, want atomic no-follow rejection", called, err)
	}
}

func TestOpenFileNoFollowRejectsTerminalRegularFileSwap(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "wanted")
	unrelated := filepath.Join(root, "unrelated")
	if err := os.WriteFile(wanted, []byte("wanted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("unrelated bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	input, err := openFileNoFollow(root, "wanted", func(component string) {
		if component != "wanted" {
			return
		}
		if renameErr := os.Rename(unrelated, wanted); renameErr != nil {
			t.Fatal(renameErr)
		}
	})
	if input != nil {
		data, _ := io.ReadAll(input)
		_ = input.Close()
		t.Fatalf("open returned substituted regular-file bytes %q", data)
	}
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("openFileNoFollow() error=%v, want identity-change rejection", err)
	}
}

func TestOpenFileNoFollowPinsOpenedParentAcrossRename(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "request")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image"), []byte("wanted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	input, err := openFileNoFollow(root, filepath.Join("request", "image"), func(component string) {
		if called || component != filepath.Join("request", "image") {
			return
		}
		called = true
		if renameErr := os.Rename(dir, dir+".old"); renameErr != nil {
			t.Fatal(renameErr)
		}
		if mkdirErr := os.Mkdir(dir, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(filepath.Join(dir, "image"), []byte("unrelated bytes"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	got, err := io.ReadAll(input)
	if err != nil {
		t.Fatal(err)
	}
	if !called || string(got) != "wanted bytes" {
		t.Fatalf("openFileNoFollow() called=%v bytes=%q, want bytes from pinned directory", called, got)
	}
}
