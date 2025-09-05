package tests

import (
	"testing"

	deletecmd "k8s-nim-operator-cli/pkg/cmd/delete"
)

func Test_Delete_Command_Wiring(t *testing.T) {
	streams, _, _, _ := genericTestIOStreams()
	cmd := deletecmd.NewDeleteCommand(nil, streams)
	if cmd.Use != "delete RESOURCE NAME" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	if len(cmd.Aliases) == 0 || cmd.Aliases[0] != "remove" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
}
