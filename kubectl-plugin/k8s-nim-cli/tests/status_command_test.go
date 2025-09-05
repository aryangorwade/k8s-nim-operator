package tests

import (
	"strings"
	"testing"

	statuscmd "k8s-nim-operator-cli/pkg/cmd/status"
)

func Test_NewStatusCommand_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := statuscmd.NewStatusCommand(nil, streams)
	if cmd.Use != "status" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	// ensure subcommands are present
	subs := cmd.Commands()
	if len(subs) < 2 {
		t.Fatalf("expected subcommands for nimcache and nimservice, got %d", len(subs))
	}
}

func Test_StatusCommand_Rejects_Invalid_Subcommands(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := statuscmd.NewStatusCommand(nil, streams)

	out, _ := executeCommandAndCaptureStdout(cmd, []string{"bogus"})
	if want := "unknown command(s) \"bogus\""; !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}

	out, _ = executeCommandAndCaptureStdout(cmd, []string{"foo", "bar"})
	if want := "unknown command(s) \"foo bar\""; !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}
}
