// Command samspel runs samspel-bench, a benchmark for agent-to-agent
// collaboration.
//
// # Subcommands
//
//	run       execute a benchmark config and write results and a report
//	report    rebuild the report from an existing results.jsonl
//	list      print task families, controls and adversary scripts
//	version   print the build version and exit
//
// Exit status is 0 on success, 1 when a subcommand fails, and 2 when the
// invocation itself is wrong: no subcommand, one this build does not know, or
// flags it cannot parse.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/agent"
	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/report"
	"github.com/noetive/samspel-bench/internal/runner"
	"github.com/noetive/samspel-bench/internal/task"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: samspel <subcommand> [flags]

subcommands:
  run       -config FILE -out DIR [-parallel N] [-mock] [-traces] [-max-jobs N] [-dry-run]
  report    -in DIR
  list      print task families, controls and adversary scripts
  version   print the build version
`

// errUsage marks a failure that is the caller's invocation, not the run.
var errUsage = errors.New("usage")

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
	var err error
	switch args[0] {
	case "version":
		// A caller reading the version from stdout must not see status 0 with
		// nothing written.
		_, err = fmt.Fprintln(stdout, version)
	case "run":
		err = cmdRun(args[1:], stdout, stderr)
	case "report":
		err = cmdReport(args[1:], stdout, stderr)
	case "list":
		err = cmdList(stdout)
	default:
		_, _ = fmt.Fprintf(stderr, "samspel: unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "samspel %s: %v\n", args[0], err)
		return 1
	}
}

// parseFlags parses a subcommand's flags, reporting a bad flag as misuse.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	return nil
}

func cmdRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/smoke.json", "benchmark config")
	out := fs.String("out", "results/run", "output directory (results.jsonl, traces/, report.md)")
	parallel := fs.Int("parallel", 0, "concurrent runs (overrides config)")
	mock := fs.Bool("mock", false, "use scripted mock agents instead of the API (no key needed)")
	mockLatency := fs.Duration("mock-latency", 5*time.Millisecond, "simulated model latency in mock mode")
	traces := fs.Bool("traces", false, "write one JSONL trace per run")
	maxJobs := fs.Int("max-jobs", 0, "run at most N pending jobs (smoke testing)")
	dry := fs.Bool("dry-run", false, "print the job count and exit")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}

	cfg, err := runner.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *parallel > 0 {
		cfg.Parallel = *parallel
	}
	jobs := runner.Expand(cfg)
	if *dry {
		_, err := fmt.Fprintf(stdout, "%d jobs (%d models x %d families x %d conditions x %d controls x instances x %d repeats)\n",
			len(jobs), len(cfg.Models), len(cfg.Families), len(cfg.Conditions), len(cfg.Controls), cfg.Repeats)
		return err
	}

	opts := runner.Options{Out: *out, Parallel: cfg.Parallel, Mock: *mock, MockLatency: *mockLatency, Traces: *traces, MaxJobs: *maxJobs, Log: slog.New(slog.NewTextHandler(stderr, nil))}
	var client *llm.Client
	if !*mock {
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return errors.New("ANTHROPIC_API_KEY is not set (use -mock to test the harness without the API)")
		}
		lim := llm.NewLimiterSet(cfg.Limits, cfg.ModelLimits)
		client = llm.NewClient(key, os.Getenv("ANTHROPIC_BASE_URL"), lim, cfg.Limits.MaxInflight)
		opts.Model = client
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	// The resolved config sits next to the results so a report can always be
	// traced back to what produced it.
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "config.json"), b, 0o644); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runErr := runner.New(cfg, opts).Run(ctx)
	if client != nil {
		_, _ = fmt.Fprintf(stderr, "samspel: API requests=%d throttled(429)=%d overloaded(529)=%d server/network errors=%d failed=%d\n",
			client.Requests.Load(), client.Throttled.Load(), client.Overloaded.Load(), client.ServerErrs.Load(), client.Failed.Load())
	}
	if err := writeReport(*out, stdout); err != nil {
		return err
	}
	if errors.Is(runErr, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, "samspel: interrupted; rerun the same command to resume")
		return nil
	}
	return runErr
}

// writeReport rebuilds report.md and report.json in dir from its
// results.jsonl and prints the markdown scorecard.
func writeReport(dir string, stdout io.Writer) error {
	res, err := report.Load(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return err
	}
	rows := report.Build(res, 1000)
	md := report.Markdown(rows)
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, md)
	return err
}

func cmdReport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	in := fs.String("in", "results/run", "directory with results.jsonl")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	return writeReport(*in, stdout)
}

func cmdList(stdout io.Writer) error {
	var w listWriter
	w.line(stdout, "Families:")
	for _, n := range task.Names() {
		f := task.Registry[n]
		w.line(stdout, fmt.Sprintf("  %s  %s (min %d agents)", n, f.Description(), f.MinAgents()))
	}
	w.line(stdout, fmt.Sprint("Controls: ", runner.Controls))
	w.line(stdout, fmt.Sprint("Adversary scripts: ", agent.Scripts))
	return w.err
}

// listWriter keeps the first write error so list reports a closed stdout
// instead of exiting 0 with nothing printed.
type listWriter struct{ err error }

func (l *listWriter) line(w io.Writer, s string) {
	if l.err == nil {
		_, l.err = fmt.Fprintln(w, s)
	}
}
