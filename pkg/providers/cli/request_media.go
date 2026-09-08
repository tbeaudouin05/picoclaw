package cliprovider

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/media"
)

var imagePathTagPattern = regexp.MustCompile(`\[image:([^\]]+)\]`)

// prepareCLIImageInputs copies image paths referenced by prompt tags into a
// request-scoped directory. CLI sandboxes can then be granted that directory
// without exposing unrelated files in PicoClaw's shared media directory.
func prepareCLIImageInputs(parts ...string) ([]string, string, func(), error) {
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	mediaRoot := filepath.Clean(media.TempDir())
	for _, part := range parts {
		for _, match := range imagePathTagPattern.FindAllStringSubmatch(part, -1) {
			path := filepath.Clean(match[1])
			if !filepath.IsAbs(path) || !pathWithin(path, mediaRoot) {
				continue
			}
			if _, ok := seen[path]; !ok {
				seen[path] = struct{}{}
				paths = append(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		return parts, "", func() {}, nil
	}
	mediaFS, err := os.OpenRoot(mediaRoot)
	if err != nil {
		return nil, "", nil, fmt.Errorf("prepare CLI image inputs: open media root: %w", err)
	}
	defer mediaFS.Close()

	dir, err := os.MkdirTemp("", "picoclaw-cli-media-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("prepare CLI image inputs: create request directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	replacements := make([]string, 0, len(paths)*2)
	for i, source := range paths {
		rel, relErr := filepath.Rel(mediaRoot, source)
		if relErr != nil {
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, relErr)
		}
		input, openErr := openRegularFileBelow(mediaFS, rel)
		if openErr != nil {
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, openErr)
		}
		target := filepath.Join(dir, fmt.Sprintf("%d-%s", i, filepath.Base(source)))
		output, createErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			_ = input.Close()
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, createErr)
		}
		_, copyErr := io.Copy(output, input)
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr != nil || closeOutErr != nil || closeInErr != nil {
			cleanup()
			if copyErr != nil {
				return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, copyErr)
			}
			if closeOutErr != nil {
				return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, closeOutErr)
			}
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, closeInErr)
		}
		replacements = append(replacements, "[image:"+source+"]", "[image:"+target+"]")
	}

	replacer := strings.NewReplacer(replacements...)
	rewritten := make([]string, len(parts))
	for i, part := range parts {
		rewritten[i] = replacer.Replace(part)
	}
	return rewritten, dir, cleanup, nil
}

// openRegularFileBelow rejects existing symlinks in every path component, then
// opens through an anchored os.Root. The rooted open is the security boundary:
// even if the tree changes after the component checks, the open cannot escape
// the media root. Regularity is checked on the opened handle to avoid a
// validation/open race on the final component.
func openRegularFileBelow(root *os.Root, name string) (*os.File, error) {
	current := ""
	for _, component := range strings.Split(name, string(filepath.Separator)) {
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlinked path component %q is not allowed", current)
		}
	}

	input, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := input.Stat()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = input.Close()
		return nil, fmt.Errorf("not a regular file")
	}
	return input, nil
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func appendAddDirs(args []string, dirs ...string) []string {
	seen := make(map[string]struct{})
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--add-dir" {
			seen[filepath.Clean(args[i+1])] = struct{}{}
			i++
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if _, ok := seen[clean]; ok {
			continue
		}
		args = append(args, "--add-dir", dir)
		seen[clean] = struct{}{}
	}
	return args
}
