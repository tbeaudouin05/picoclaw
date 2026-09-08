package cliprovider

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

func useTestMediaDir(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	dir := media.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrepareCLIImageInputsScopesAndRewritesRelevantImages(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "netbsd" {
		t.Skip("managed CLI images fail closed without atomic no-follow platform support")
	}
	sourceDir := useTestMediaDir(t)
	first := filepath.Join(sourceDir, "first.png")
	second := filepath.Join(sourceDir, "second.jpg")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, dir, cleanup, err := prepareCLIImageInputs(
		"look [image:"+first+"] and [image: photo]",
		"again [image:"+first+"] then [image:"+second+"]",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if dir == "" || filepath.Clean(dir) == filepath.Clean(sourceDir) {
		t.Fatalf("request media dir = %q, want a distinct scoped directory", dir)
	}
	if strings.Contains(strings.Join(prepared, "\n"), first) || strings.Contains(strings.Join(prepared, "\n"), second) {
		t.Fatalf("prepared prompts retained source paths: %q", prepared)
	}
	if !strings.Contains(prepared[0], "[image: photo]") {
		t.Fatalf("non-path image placeholder changed: %q", prepared[0])
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("scoped directory has %d files, want two unique inputs", len(entries))
	}
}

func TestPrepareCLIImageInputsRewritesRawSpellingsAndDeduplicatesCopy(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "netbsd" {
		t.Skip("managed CLI images fail closed without atomic no-follow platform support")
	}
	sourceDir := useTestMediaDir(t)
	subdir := filepath.Join(sourceDir, "sub")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "image.png")
	if err := os.WriteFile(source, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawPaths := []string{
		sourceDir + string(filepath.Separator) + "." + string(filepath.Separator) + "image.png",
		subdir + string(filepath.Separator) + ".." + string(filepath.Separator) + "image.png",
	}
	if filepath.Separator == '/' {
		rawPaths = append(rawPaths, sourceDir+"//image.png")
	}
	parts := make([]string, len(rawPaths))
	for i, rawPath := range rawPaths {
		parts[i] = "see [image:" + rawPath + "]"
	}

	prepared, dir, cleanup, err := prepareCLIImageInputs(parts...)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("scoped directory has %d files, want one deduplicated input", len(entries))
	}
	targetTag := "[image:" + filepath.Join(dir, entries[0].Name()) + "]"
	for i, prompt := range prepared {
		if !strings.Contains(prompt, targetTag) || strings.Contains(prompt, rawPaths[i]) {
			t.Fatalf("prepared[%d] = %q, want rewritten tag %q", i, prompt, targetTag)
		}
	}
}

func TestPrepareCLIImageInputsLeavesPromptWithoutManagedMediaUnchanged(t *testing.T) {
	parts := []string{"plain prompt", "placeholder [image: photo]", "workspace [image:/workspace/photo.png]"}
	prepared, dir, cleanup, err := prepareCLIImageInputs(parts...)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if dir != "" || strings.Join(prepared, "\x00") != strings.Join(parts, "\x00") {
		t.Fatalf("prepared = %q, dir = %q; want unchanged prompt and no extra directory", prepared, dir)
	}
}

func TestPrepareCLIImageInputsRejectsSymlinkedParentEscape(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "netbsd" {
		t.Skip("managed CLI images fail closed without atomic no-follow platform support")
	}
	mediaDir := useTestMediaDir(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(mediaDir, "redirect")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not supported: %v", err)
	}

	_, _, _, err := prepareCLIImageInputs("see [image:" + filepath.Join(link, "secret.png") + "]")
	if err == nil || !strings.Contains(err.Error(), "non-symlink path component") {
		t.Fatalf("prepareCLIImageInputs() error = %v, want symlinked-component rejection", err)
	}
}

func TestAppendAddDirsPairsAndDeduplicates(t *testing.T) {
	args := appendAddDirs([]string{"--sandbox", "--add-dir", "/workspace"}, "/workspace/.", "/media", "/media")
	want := []string{"--sandbox", "--add-dir", "/workspace", "--add-dir", "/media"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q, want paired deduplicated flags %q", args, want)
	}
}

func TestCLIProviderImageCopyLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		stream   bool
	}{
		{name: "claude/json", provider: "claude"},
		{name: "claude/stream-json", provider: "claude", stream: true},
		{name: "antigravity/json", provider: "antigravity"},
		{name: "antigravity/stream-json", provider: "antigravity", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("mock CLI scripts not supported on Windows")
			}
			t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "temp root with spaces"))
			mediaDir := media.TempDir()
			if err := os.MkdirAll(mediaDir, 0o700); err != nil {
				t.Fatal(err)
			}
			image := filepath.Join(mediaDir, "image with spaces.png")
			if err := os.WriteFile(image, []byte("image bytes"), 0o600); err != nil {
				t.Fatal(err)
			}

			state := t.TempDir()
			mock := createBlockingMediaCLI(t, state, tc.provider, tc.stream)
			messages := []Message{{Role: "user", Content: "see [image:" + image + "]"}}
			done := make(chan error, 1)
			go func() {
				if tc.provider == "claude" {
					p := NewClaudeCliProvider("")
					p.command = mock
					if tc.stream {
						_, err := p.ChatStreamEvents(context.Background(), messages, nil, "", nil, nil)
						done <- err
					} else {
						_, err := p.Chat(context.Background(), messages, nil, "", nil)
						done <- err
					}
					return
				}
				p := NewAntigravityCliProvider("")
				p.command = mock
				if tc.stream {
					_, err := p.ChatStreamEvents(context.Background(), messages, nil, "", nil, nil)
					done <- err
				} else {
					_, err := p.Chat(context.Background(), messages, nil, "", nil)
					done <- err
				}
			}()

			waitForFIFO(t, filepath.Join(state, "ready"))
			args := readArgLines(t, filepath.Join(state, "args"))
			scopedDir := singleAddDir(t, args, image)
			entries, err := os.ReadDir(scopedDir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("scoped directory while child runs: entries=%v err=%v", entries, err)
			}
			copiedPath := filepath.Join(scopedDir, entries[0].Name())
			prompt, err := os.ReadFile(filepath.Join(state, "prompt"))
			if err != nil {
				t.Fatal(err)
			}
			if want := "[image:" + copiedPath + "]"; !strings.Contains(string(prompt), want) {
				t.Fatalf("child prompt = %q, want exact rewritten tag %q", prompt, want)
			}
			copied, err := os.ReadFile(filepath.Join(state, "copied"))
			if err != nil || string(copied) != "image bytes" {
				t.Fatalf("copy read by running child = %q, %v", copied, err)
			}

			releaseFIFO(t, filepath.Join(state, "release"))
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(scopedDir); !os.IsNotExist(err) {
				t.Fatalf("scoped directory remains after child completion: %v", err)
			}
		})
	}
}

func TestCLIProvidersFailImagePreparationBeforeLaunch(t *testing.T) {
	missing := filepath.Join(useTestMediaDir(t), "missing.png")
	marker := filepath.Join(t.TempDir(), "launched")
	script := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	messages := []Message{{Role: "user", Content: "see [image:" + missing + "]"}}
	for _, tc := range []struct {
		name     string
		provider string
		stream   bool
	}{
		{name: "claude/json", provider: "claude"},
		{name: "claude/stream-json", provider: "claude", stream: true},
		{name: "antigravity/json", provider: "antigravity"},
		{name: "antigravity/stream-json", provider: "antigravity", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runCLIProvider(t, tc.provider, tc.stream, script, messages)
			if err == nil || !strings.Contains(err.Error(), "prepare CLI image input") {
				t.Fatalf("provider error = %v, want explicit preparation error", err)
			}
		})
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("subprocess launched before preparation failure; marker stat error = %v", err)
	}
}

func TestCLIProviderSubprocessFailuresInAllMediaModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	image := filepath.Join(useTestMediaDir(t), "image.png")
	if err := os.WriteFile(image, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "failing-cli")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	messages := []Message{{Role: "user", Content: "see [image:" + image + "]"}}
	for _, tc := range []struct {
		name     string
		provider string
		stream   bool
	}{
		{name: "claude/json", provider: "claude"},
		{name: "claude/stream-json", provider: "claude", stream: true},
		{name: "antigravity/json", provider: "antigravity"},
		{name: "antigravity/stream-json", provider: "antigravity", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runCLIProvider(t, tc.provider, tc.stream, script, messages); err == nil {
				t.Fatal("provider returned nil error for failing subprocess")
			}
		})
	}
}

func runCLIProvider(t *testing.T, provider string, stream bool, command string, messages []Message) error {
	t.Helper()
	if provider == "claude" {
		p := NewClaudeCliProvider("")
		p.command = command
		if stream {
			_, err := p.ChatStreamEvents(context.Background(), messages, nil, "", nil, nil)
			return err
		}
		_, err := p.Chat(context.Background(), messages, nil, "", nil)
		return err
	}
	p := NewAntigravityCliProvider("")
	p.command = command
	if stream {
		_, err := p.ChatStreamEvents(context.Background(), messages, nil, "", nil, nil)
		return err
	}
	_, err := p.Chat(context.Background(), messages, nil, "", nil)
	return err
}

func createBlockingMediaCLI(t *testing.T, state, provider string, stream bool) string {
	t.Helper()
	for _, name := range []string{"ready", "release"} {
		if err := exec.Command("mkfifo", filepath.Join(state, name)).Run(); err != nil {
			t.Skipf("mkfifo is unavailable: %v", err)
		}
	}
	output := `{"type":"result","result":"ok","session_id":"test"}`
	if provider == "claude" && stream {
		output = `{"type":"result","result":"ok"}`
	}
	if provider == "antigravity" {
		output = `{"status":"SUCCESS","response":"ok"}`
		if stream {
			output = `{"event":"result","result":{"status":"SUCCESS","response":"ok"}}`
		}
	}
	script := filepath.Join(state, provider)
	contents := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %q
dir=
previous=
for arg do
	if [ "$previous" = "--add-dir" ]; then dir=$arg; fi
	case "$arg" in --print=*) printf '%%s' "${arg#--print=}" > %q;; esac
	previous=$arg
done
if [ %q = claude ]; then cat > %q; fi
set -- "$dir"/*
cat "$1" > %q
printf ready > %q
cat %q >/dev/null
printf '%%s\n' %q
`, filepath.Join(state, "args"), filepath.Join(state, "prompt"), provider,
		filepath.Join(state, "prompt"), filepath.Join(state, "copied"),
		filepath.Join(state, "ready"), filepath.Join(state, "release"), output)
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func waitForFIFO(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || string(data) != "ready" {
		t.Fatalf("ready handshake = %q, read err=%v close err=%v", data, readErr, closeErr)
	}
}

func releaseFIFO(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readArgLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func singleAddDir(t *testing.T, args []string, sourceImage string) string {
	t.Helper()
	count := 0
	var dir string
	for i := range args {
		if args[i] == "--add-dir" {
			count++
			if i+1 >= len(args) {
				t.Fatalf("unpaired --add-dir in %q", args)
			}
			dir = args[i+1]
		}
	}
	if count != 1 {
		t.Fatalf("--add-dir count = %d, want one in %q", count, args)
	}
	if filepath.Clean(dir) == filepath.Clean(filepath.Dir(sourceImage)) {
		t.Fatalf("granted source/global directory %q instead of request-scoped directory", dir)
	}
	return dir
}
