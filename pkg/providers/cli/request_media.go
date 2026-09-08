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

const (
	// Bound the temporary disk used to expose managed images to one CLI request.
	// Ten 10 MiB images fit within the byte ceiling, while fewer larger images
	// remain supported up to the same 100 MiB aggregate limit.
	maxCLIImageInputs     = 10
	maxCLIImageInputBytes = int64(100 * 1024 * 1024)
)

type cliImageInputLimits struct {
	maxFiles int
	maxBytes int64
}

// prepareCLIImageInputs copies image paths referenced by prompt tags into a
// request-scoped directory. CLI sandboxes can then be granted that directory
// without exposing unrelated files in PicoClaw's shared media directory.
func prepareCLIImageInputs(parts ...string) ([]string, string, func(), error) {
	return prepareCLIImageInputsWithLimits(cliImageInputLimits{
		maxFiles: maxCLIImageInputs,
		maxBytes: maxCLIImageInputBytes,
	}, parts...)
}

// prepareCLIImageInputsWithLimits exists so tests can exercise the byte limit
// without creating production-sized files.
func prepareCLIImageInputsWithLimits(limits cliImageInputLimits, parts ...string) ([]string, string, func(), error) {
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	rawTags := make(map[string][]string)
	seenRawTags := make(map[string]map[string]struct{})
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
				seenRawTags[path] = make(map[string]struct{})
			}
			if _, ok := seenRawTags[path][match[0]]; !ok {
				seenRawTags[path][match[0]] = struct{}{}
				rawTags[path] = append(rawTags[path], match[0])
			}
		}
	}
	if len(paths) == 0 {
		return parts, "", func() {}, nil
	}
	if len(paths) > limits.maxFiles {
		return nil, "", nil, fmt.Errorf("prepare CLI image inputs: %d unique images exceed the per-request limit of %d", len(paths), limits.maxFiles)
	}
	dir, err := os.MkdirTemp("", "picoclaw-cli-media-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("prepare CLI image inputs: create request directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	replacements := make([]string, 0, len(paths)*2)
	var copiedBytes int64
	for i, source := range paths {
		rel, relErr := filepath.Rel(mediaRoot, source)
		if relErr != nil {
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, relErr)
		}
		input, openErr := openRegularFileBelow(mediaRoot, rel)
		if openErr != nil {
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, openErr)
		}
		info, statErr := input.Stat()
		if statErr != nil {
			_ = input.Close()
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: determine size: %w", source, statErr)
		}
		remaining := limits.maxBytes - copiedBytes
		if info.Size() > remaining {
			_ = input.Close()
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: request images exceed the %d-byte total limit", source, limits.maxBytes)
		}
		target := filepath.Join(dir, fmt.Sprintf("%d-%s", i, filepath.Base(source)))
		output, createErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			_ = input.Close()
			cleanup()
			return nil, "", nil, fmt.Errorf("prepare CLI image input %q: %w", source, createErr)
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr == nil && written > remaining {
			copyErr = fmt.Errorf("request images exceed the %d-byte total limit", limits.maxBytes)
		}
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
		copiedBytes += written
		for _, rawTag := range rawTags[source] {
			replacements = append(replacements, rawTag, "[image:"+target+"]")
		}
	}

	replacer := strings.NewReplacer(replacements...)
	rewritten := make([]string, len(parts))
	for i, part := range parts {
		rewritten[i] = replacer.Replace(part)
	}
	return rewritten, dir, cleanup, nil
}

// openRegularFileBelow delegates the security-sensitive path traversal to a
// platform helper. Regularity is checked on the opened handle, never by name.
func openRegularFileBelow(root, name string) (*os.File, error) {
	input, err := openFileNoFollow(root, name, nil)
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
