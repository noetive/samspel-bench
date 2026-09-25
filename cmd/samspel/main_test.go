package main

import (
	"bytes"
	"strings"
	"testing"
)

// A script that shells out to samspel branches on the exit status, so a wrong
// invocation has to be distinguishable from a successful one without parsing
// output, and the output a human sees has to say what to do next.
func TestRun_ExitStatusSeparatesSuccessFromMisuse(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStatus int
		wantStdout string
		wantStderr string
	}{
		{name: "version prints the build version", args: []string{"version"}, wantStatus: 0, wantStdout: version + "\n"},
		{name: "no subcommand prints usage", args: nil, wantStatus: 2, wantStderr: "usage: samspel"},
		{name: "unknown subcommand is named and usage follows", args: []string{"bogus"}, wantStatus: 2, wantStderr: `unknown subcommand "bogus"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tc.args, &stdout, &stderr)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if stdout.String() != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.wantStdout)
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
			if tc.wantStatus != 0 && !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("misuse must print usage, stderr = %q", stderr.String())
			}
		})
	}
}
