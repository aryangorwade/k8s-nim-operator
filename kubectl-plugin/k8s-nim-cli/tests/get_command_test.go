package tests

import (
	"strings"
	"testing"

	getcmd "k8s-nim-operator-cli/pkg/cmd/get"
)

func Test_NewGetCommand_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := getcmd.NewGetCommand(nil, streams)
	if cmd.Use != "get" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "list" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
	// ensure subcommands are present
	subs := cmd.Commands()
	var hasCache, hasSvc bool
	for _, c := range subs {
		switch c.Name() {
		case "nimcache":
			hasCache = true
		case "nimservice":
			hasSvc = true
		}
	}
	if !hasCache || !hasSvc {
		t.Fatalf("expected nimcache and nimservice subcommands")
	}
}

func Test_Get_NIMCache_Subcommand_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := getcmd.NewGetNIMCacheCommand(nil, streams)
	if cmd.Use != "nimcache [NAME]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "nimcaches" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
	if f := cmd.Flags().Lookup("all-namespaces"); f == nil {
		t.Fatalf("expected all-namespaces flag")
	}
}

func Test_Get_NIMService_Subcommand_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := getcmd.NewGetNIMServiceCommand(nil, streams)
	if cmd.Use != "nimservice [NAME]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "nimservices" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
	if f := cmd.Flags().Lookup("all-namespaces"); f == nil {
		t.Fatalf("expected all-namespaces flag")
	}
}

func Test_GetCommand_Rejects_Invalid_Subcommands(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := getcmd.NewGetCommand(nil, streams)

	out, _ := executeCommandAndCaptureStdout(cmd, []string{"bogus"})
	if want := "unknown command(s) \"bogus\""; !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}

	out, _ = executeCommandAndCaptureStdout(cmd, []string{"foo", "bar"})
	if want := "unknown command(s) \"foo bar\""; !strings.Contains(out, want) {
		t.Fatalf("expected output to contain %q, got: %s", want, out)
	}
}
