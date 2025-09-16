package tests

import (
	"bytes"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"

	rootcmd "k8s-nim-operator-cli/pkg/cmd"
)

func Test_NewNIMCommand_Wiring(t *testing.T) {
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	cmd := rootcmd.NewNIMCommand(streams)
	if cmd.Use != "nim" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	// ensure main subcommands exist
	want := []string{"get", "status", "logs", "delete", "deploy"}
	gotNames := map[string]bool{}
	for _, c := range cmd.Commands() {
		gotNames[c.Name()] = true
	}
	for _, w := range want {
		if !gotNames[w] {
			t.Fatalf("expected subcommand %q present", w)
		}
	}
	// ensure kube flags were added (hidden), not asserting hidden here
	if cmd.PersistentFlags().Lookup("kubeconfig") == nil {
		t.Fatalf("expected kubeconfig flag present")
	}
}
