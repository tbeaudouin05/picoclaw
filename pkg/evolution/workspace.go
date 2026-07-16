package evolution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalWorkspace normalizes a workspace path to a single canonical identity
// so equivalent spellings — a leading "~"/"~/" alias, relative versus absolute
// forms, dot segments, redundant separators — resolve to one actual
// workspace/state path.
//
// It deliberately does NOT resolve symlinks: intentionally distinct configured
// paths that happen to share a symlink or mount target must stay distinct, and
// a path that does not yet exist must still canonicalize. Case is preserved so
// two case-different paths on a case-sensitive filesystem remain distinct.
//
// An empty (or whitespace-only) input canonicalizes to "" so callers keep their
// existing "no workspace" short-circuits. A home-directory lookup failure while
// expanding a leading "~" is returned as an error rather than silently
// producing a wrong relative path.
func CanonicalWorkspace(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}

	expanded, err := expandWorkspaceHome(trimmed)
	if err != nil {
		return "", err
	}

	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %q: %w", path, err)
	}
	// filepath.Abs already cleans, but keep Clean explicit for intent.
	return filepath.Clean(abs), nil
}

// expandWorkspaceHome expands only a leading standalone "~" or "~/" using the
// current user's home directory, matching the agent's expandHome semantics. It
// never expands "~otheruser" — that is treated as a literal relative segment.
func expandWorkspaceHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	if len(path) > 1 && path[1] != '/' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand ~ in workspace path %q: %w", path, err)
	}
	if len(path) == 1 {
		return home, nil
	}
	return home + path[1:], nil
}
