package tests

import (
	"strings"
	"testing"

	deploycmd "k8s-nim-operator-cli/pkg/cmd/deploy"
)

func Test_NewDeployCommand_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := deploycmd.NewDeployCommand(nil, streams)
	if cmd.Use != "deploy" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "create" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
	// ensure subcommands are present
	if len(cmd.Commands()) < 2 {
		t.Fatalf("expected NIMCache and NIMService subcommands")
	}
}

func Test_DeployCommand_Rejects_Invalid_Subcommands(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := deploycmd.NewDeployCommand(nil, streams)

	out, _ := executeCommandAndCaptureStdout(cmd, []string{"bogus"})
	if want := " Error: unknown command(s) \"bogus\""; !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}

	out, _ = executeCommandAndCaptureStdout(cmd, []string{"foo", "bar"})
	if want := " Error: unknown command(s) \"foo bar\""; !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}
}