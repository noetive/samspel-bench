// Command samspel runs samspel-bench, a benchmark for agent-to-agent
// collaboration.
//
// # Subcommands
//
//	version   print the build version and exit
//
// Exit status is 0 on success, 1 when a subcommand fails, and 2 when the
// invocation itself is wrong: no subcommand, or one this build does not know.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: samspel <subcommand>

subcommands:
  version   print the build version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches one invocation and returns its exit status. It takes its
// arguments and writers as parameters so a test can drive it without a
// subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	// Writes to stderr are best effort: when the diagnostic channel itself is
	// gone there is nowhere left to report that, and the exit status still
	// carries the outcome.
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		// A caller reading the version from stdout must not see status 0 with
		// nothing written.
		if _, err := fmt.Fprintln(stdout, version); err != nil {
			_, _ = fmt.Fprintf(stderr, "samspel version: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "samspel: unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
}
