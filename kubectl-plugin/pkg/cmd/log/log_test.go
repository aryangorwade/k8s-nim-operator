package log

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func Test_parseArtifactDir_prefersStdoutThenStderr(t *testing.T) {
	out1 := []byte("some output... ARTIFACT_DIR=/tmp/bundle-123 ... done\n")
	out2 := []byte("+ export ARTIFACT_DIR=/tmp/bundle-456\n")
	if got := parseArtifactDir(out1, out2); got != "/tmp/bundle-123" {
		t.Fatalf("parseArtifactDir prefer stdout got %q", got)
	}

	// No stdout match → fallback to stderr scanner format
	out1 = []byte("no artifact here")
	if got := parseArtifactDir(out1, out2); got != "/tmp/bundle-456" {
		t.Fatalf("parseArtifactDir fallback stderr got %q", got)
	}

	// Neither side present → empty
	if got := parseArtifactDir([]byte(""), []byte("")); got != "" {
		t.Fatalf("expected empty when not present, got %q", got)
	}
}

func Test_findArtifactDirIn_matchesRegexAndXtrace(t *testing.T) {
	// Regex form
	b := []byte("prep... ARTIFACT_DIR=/tmp/abc def ...\n")
	if got := findArtifactDirIn(b); got != "/tmp/abc" {
		t.Fatalf("regex path got %q", got)
	}
	// Xtrace export line
	b = []byte("some\n+ export ARTIFACT_DIR=/tmp/xyz\nend\n")
	if got := findArtifactDirIn(b); got != "/tmp/xyz" {
		t.Fatalf("xtrace path got %q", got)
	}
}

func Test_listResourceLogPaths_happyPathAndOrdering(t *testing.T) {
	root := t.TempDir()
	nimDir := filepath.Join(root, "nim")
	if err := os.MkdirAll(nimDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Create files with different mtimes
	paths := []string{
		filepath.Join(nimDir, "a.log"),
		filepath.Join(nimDir, "b.log"),
		filepath.Join(nimDir, "c.txt"), // non-log should be ignored
	}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// Set mtimes: b.log newest, a.log older
	old := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(paths[0], old, old); err != nil {
		t.Fatalf("chtimes a: %v", err)
	}
	if err := os.Chtimes(paths[1], newer, newer); err != nil {
		t.Fatalf("chtimes b: %v", err)
	}

	got, err := listResourceLogPaths(root)
	if err != nil {
		t.Fatalf("listResourceLogPaths error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 .log files, got %d: %v", len(got), got)
	}
	// Newest first
	if !strings.HasSuffix(got[0], "b.log") || !strings.HasSuffix(got[1], "a.log") {
		t.Fatalf("order wrong: %v", got)
	}
}

func Test_listResourceLogPaths_errors(t *testing.T) {
	root := t.TempDir()
	// No nim dir → error
	if _, err := listResourceLogPaths(root); err == nil {
		t.Fatalf("expected error when nim directory missing")
	}
	// nim dir present but no .log files → error
	nimDir := filepath.Join(root, "nim")
	if err := os.MkdirAll(nimDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nimDir, "note.txt"), []byte("n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := listResourceLogPaths(root); err == nil {
		t.Fatalf("expected error when no .log files present")
	}
}

func Test_NewLogCommand_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := NewLogCommand(nil, streams)
	if !strings.HasPrefix(cmd.Use, "logs ") {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if cmd.Aliases == nil || len(cmd.Aliases) == 0 || cmd.Aliases[0] != "log" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
}

// minimal test IOStreams without importing cli-runtime test helpers
func genericTestIOStreams() (s genericclioptions.IOStreams, in *bytes.Buffer, out *bytes.Buffer, errOut *bytes.Buffer) {
	in = &bytes.Buffer{}
	out = &bytes.Buffer{}
	errOut = &bytes.Buffer{}
	return genericclioptions.IOStreams{In: in, Out: out, ErrOut: errOut}, in, out, errOut
}
