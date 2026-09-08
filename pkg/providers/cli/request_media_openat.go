//go:build linux || darwin || netbsd

package cliprovider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openFileNoFollow walks name one component at a time through directory file
// descriptors. O_NOFOLLOW makes each open and its symlink check one operation;
// holding the parent descriptor also prevents a concurrent rename from
// redirecting the remainder of the walk to a different directory.
//
// beforeOpen is a test seam invoked after the parent is pinned and immediately
// before each child open. Production callers pass nil.
func openFileNoFollow(root, name string, beforeOpen func(string)) (*os.File, error) {
	components, err := cleanRelativeComponents(name)
	if err != nil {
		return nil, err
	}

	fd, err := openAbsoluteDirectoryNoFollow(root)
	if err != nil {
		return nil, fmt.Errorf("open media root: %w", err)
	}
	current := ""
	for i, component := range components {
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		var before unix.Stat_t
		if statErr := unix.Fstatat(fd, component, &before, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("stat path component %q: %w", current, statErr)
		}
		if beforeOpen != nil {
			beforeOpen(current)
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if i != len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, component, flags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("open non-symlink path component %q: %w", current, openErr)
		}
		var after unix.Stat_t
		if statErr := unix.Fstat(next, &after); statErr != nil {
			_ = unix.Close(next)
			return nil, fmt.Errorf("stat opened path component %q: %w", current, statErr)
		}
		if before.Dev != after.Dev || before.Ino != after.Ino {
			_ = unix.Close(next)
			return nil, fmt.Errorf("path component %q changed while it was opened", current)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Base(name)), nil
}

// openAbsoluteDirectoryNoFollow follows aliases in the absolute parent path so
// platform-controlled spellings such as macOS /var can be used. The terminal
// directory itself is opened without following a symlink, identity-checked,
// and returned pinned. No stronger guarantee is made about absolute ancestors.
func openAbsoluteDirectoryNoFollow(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("root path %q is not absolute", path)
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) {
		return unix.Open(clean, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	parent, base := filepath.Split(clean)
	parentFD, err := unix.Open(filepath.Clean(parent), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open media root parent: %w", err)
	}
	defer unix.Close(parentFD)
	var before unix.Stat_t
	if err := unix.Fstatat(parentFD, base, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, fmt.Errorf("stat media root: %w", err)
	}
	fd, err := unix.Openat(parentFD, base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open non-symlink media root: %w", err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("stat opened media root: %w", err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("media root changed while it was opened")
	}
	return fd, nil
}

func cleanRelativeComponents(name string) ([]string, error) {
	if name == "" || filepath.IsAbs(name) {
		return nil, fmt.Errorf("invalid relative media path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("media path %q is outside the media root", name)
	}
	return strings.Split(clean, string(filepath.Separator)), nil
}
