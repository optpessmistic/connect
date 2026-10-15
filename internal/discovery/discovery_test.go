package discovery

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"howett.net/plist"
)

func file(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// File permission bits follow the host filesystem, so ordinary discovery
// fixtures must use the host's executable naming and permission rules.
func executableName(id string) string {
	if runtime.GOOS == "windows" {
		return id + ".exe"
	}
	return id
}

func TestMetadataAndCodexNeverExecuted(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	for _, id := range []string{"codex", "claude", "omp", "opencode", "kimi"} {
		file(t, filepath.Join(bin, executableName(id)), "executable", 0700)
	}
	file(t, filepath.Join(bin, "node_modules", "@openai", "codex", "package.json"), `{"name":"@openai/codex","version":"0.123.0"}`, 0600)
	file(t, filepath.Join(bin, "node_modules", "@anthropic-ai", "claude-code", "package.json"), `{"name":"@anthropic-ai/claude-code","version":"2.5.1"}`, 0600)
	options := Options{Home: root, OS: runtime.GOOS, Dirs: []string{bin}, Run: func(path string, dirs []string, home string) (string, string) {
		if filepath.Base(path) == executableName("codex") || filepath.Base(path) == executableName("claude") {
			t.Error("metadata client executed")
		}
		return "tool v1.2.3", "ok"
	}}
	found := Detect(options)
	if found["codex"].Version != "0.123.0" || found["claude"].Version != "2.5.1" || found["omp"].Version != "1.2.3" {
		t.Fatal("incorrect versions")
	}
	if err := os.Remove(filepath.Join(bin, "node_modules", "@openai", "codex", "package.json")); err != nil {
		t.Fatal(err)
	}
	found = Detect(options)
	if found["codex"].VersionStatus != "protected" || found["codex"].Version != "" {
		t.Fatal("Codex without metadata must remain protected")
	}
}
func TestPathPriority(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "path")
	fallback := filepath.Join(root, "fallback")
	file(t, filepath.Join(first, executableName("claude")), "executable", 0700)
	file(t, filepath.Join(fallback, executableName("claude")), "executable", 0700)
	p, _ := find("claude", Options{OS: runtime.GOOS, Dirs: []string{first, fallback}})
	if p != filepath.Join(first, executableName("claude")) {
		t.Fatal("PATH priority lost")
	}
}

func TestWindowsShimNeverExecuted(t *testing.T) {
	for _, ext := range []string{".cmd", ".bat"} {
		t.Run(ext, func(t *testing.T) {
			root := t.TempDir()
			file(t, filepath.Join(root, "opencode"+ext), "shim", 0600)
			options := Options{Home: root, OS: "windows", Dirs: []string{root}, Run: func(string, []string, string) (string, string) {
				t.Error("Windows shim interpreted by shell")
				return "", "failed"
			}}
			found := Detect(options)
			if !found["opencode"].Installed || found["opencode"].Version != "" || found["opencode"].VersionStatus != "unknown" {
				t.Fatal("shim without package metadata must have an unknown version")
			}
			file(t, filepath.Join(root, "node_modules", "opencode-ai", "package.json"), `{"name":"opencode-ai","version":"2.1.0"}`, 0600)
			found = Detect(options)
			if found["opencode"].Version != "2.1.0" || found["opencode"].VersionStatus != "metadata" {
				t.Fatal("Windows package version missed")
			}
		})
	}
}

func TestWindowsExecutablePriority(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	fallback := filepath.Join(root, "fallback")
	for _, ext := range []string{".bat", ".cmd", ".exe"} {
		file(t, filepath.Join(first, "claude"+ext), "fixture", 0600)
	}
	file(t, filepath.Join(fallback, "claude.exe"), "fixture", 0600)
	options := Options{OS: "windows", Dirs: []string{first, fallback}}
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		want := filepath.Join(first, "claude"+ext)
		if got, _ := find("claude", options); got != want {
			t.Fatalf("expected %s, got %s", want, got)
		}
		if err := os.Remove(want); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := find("claude", options); got != filepath.Join(fallback, "claude.exe") {
		t.Fatal("fallback executable not found")
	}
}

func TestWrongPackageNotTrustedAndTimeoutDistinguished(t *testing.T) {
	root := t.TempDir()
	file(t, filepath.Join(root, executableName("kimi")), "executable", 0700)
	file(t, filepath.Join(root, "node_modules", "@moonshot-ai", "kimi-code", "package.json"), `{"name":"unrelated","version":"99.0.0"}`, 0600)
	found := Detect(Options{Home: root, OS: runtime.GOOS, Dirs: []string{root}, Run: func(string, []string, string) (string, string) { return "", "timeout" }})
	if !found["kimi"].Installed || found["kimi"].Version != "" || found["kimi"].VersionStatus != "timeout" || found["omp"].Installed {
		t.Fatal("timeout or missing status conflated")
	}
}
func TestDirectoriesIncludeGUIFallbacksWithoutRelativePATH(t *testing.T) {
	root := t.TempDir()
	env := func(name string) string {
		if name == "PATH" {
			return "." + string(os.PathListSeparator) + filepath.Join(root, "custom")
		}
		if name == "APPDATA" {
			return filepath.Join(root, "roaming")
		}
		return ""
	}
	dirs := Directories(root, "windows", env)
	joined := strings.Join(dirs, "\n")
	for _, want := range []string{filepath.Join(root, "custom"), filepath.Join(root, ".local", "bin"), filepath.Join(root, "roaming", "npm"), filepath.Join(root, ".bun", "bin")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			t.Fatal("relative PATH accepted")
		}
	}
}

func TestPassiveDesktopMetadata(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Applications", "Codex.app")
	file(t, filepath.Join(app, "Contents", "Info.plist"), `<?xml version="1.0"?><plist><dict><key>CFBundleShortVersionString</key><string>9.8.7</string></dict></plist>`, 0600)
	found := Detect(Options{Home: root, OS: "darwin", Run: func(string, []string, string) (string, string) { t.Fatal("Desktop must not execute"); return "", "" }})
	if found["codex"].Desktop == nil || found["codex"].Desktop.Version != "9.8.7" {
		t.Fatal("desktop metadata not read")
	}
}
func TestOutputBound(t *testing.T) {
	var output limitedOutput
	chunk := []byte(strings.Repeat("x", 64000))
	n, err := output.Write(chunk)
	if err != nil || n != len(chunk) || output.b.Len() != 32768 {
		t.Fatal("probe output not bounded")
	}
}

func TestChatGPTDesktopBinaryPlist(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Applications", "ChatGPT.app")
	data, err := plist.Marshal(map[string]string{"CFBundleShortVersionString": "2026.1009.402", "CFBundleVersion": "9876"}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	file(t, filepath.Join(app, "Contents", "Info.plist"), string(data), 0600)
	info := desktop(root)
	if info.Name != "ChatGPT" || info.Version != "2026.1009.402" || !info.Installed {
		t.Fatal("ChatGPT desktop metadata missed")
	}
}
