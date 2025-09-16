package tests

import (
	"bytes"
	"io"
	"os"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"github.com/spf13/cobra"
)

// Shared IOStreams helper for tests in this package
func genericTestIOStreams() (s genericclioptions.IOStreams, in *bytes.Buffer, out *bytes.Buffer, errOut *bytes.Buffer) {
	in = &bytes.Buffer{}
	out = &bytes.Buffer{}
	errOut = &bytes.Buffer{}
	return genericclioptions.IOStreams{In: in, Out: out, ErrOut: errOut}, in, out, errOut
}

// executeCommandAndCaptureStdout runs the provided cobra command with args and captures
// anything written to os.Stdout during execution, returning the captured output and error from Execute.
func executeCommandAndCaptureStdout(cmd *cobra.Command, args []string) (string, error) {
	// Preserve original stdout
	origStdout := os.Stdout
	defer func() { os.Stdout = origStdout }()

	// Pipe to capture stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w

	// Ensure cobra writes go to stdout so we capture them
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stdout)
	cmd.SetArgs(args)

	execErr := cmd.Execute()

	// Close writer and restore stdout before reading
	_ = w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()

	return buf.String(), execErr
}
