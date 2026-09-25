package main

import (
	"bytes"
	"os"
	"path/filepath"
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

// A mistyped flag is the caller's mistake, not a failed run, so a script must
// see status 2 and the usage it needs to correct the invocation.
func TestRun_BadFlagIsMisuse(t *testing.T) {
	for _, sub := range []string{"run", "report"} {
		t.Run(sub, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if status := run([]string{sub, "-no-such-flag"}, &stdout, &stderr); status != 2 {
				t.Fatalf("status = %d, want 2", status)
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

// list is how a user discovers what a config may name, so every registered
// family, control and adversary script has to appear in it.
func TestRun_ListNamesEverythingAConfigCanReference(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"list"}, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	for _, want := range []string{"F1", "F5", "F8", "team", "oracle", "confident_wrong", "injector"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("list output lacks %q:\n%s", want, stdout.String())
		}
	}
}

// A mock run exercises the whole pipeline without an API key: it must leave
// results, a report and the resolved config on disk, and a rerun must resume
// rather than repeat work, because interrupted runs are resumed that way.
func TestRun_MockRunWritesArtifactsAndResumes(t *testing.T) {
	out := t.TempDir()
	args := []string{"run", "-config", "../../configs/mock-all.json", "-out", out, "-mock", "-mock-latency", "0s", "-max-jobs", "6"}
	var stdout, stderr bytes.Buffer
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	for _, name := range []string{"results.jsonl", "report.md", "report.json", "config.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	first, err := os.ReadFile(filepath.Join(out, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(first, []byte("\n")); n != 6 {
		t.Fatalf("results lines = %d, want 6", n)
	}

	stdout.Reset()
	stderr.Reset()
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("resume status = %d, stderr = %q", status, stderr.String())
	}
	second, err := os.ReadFile(filepath.Join(out, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(second, first) {
		t.Fatal("resume rewrote earlier results")
	}
}
