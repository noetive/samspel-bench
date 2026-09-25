# samspel-bench

[![ci](https://github.com/noetive/samspel-bench/actions/workflows/ci.yml/badge.svg)](https://github.com/noetive/samspel-bench/actions/workflows/ci.yml)

Measure how well your agents work together, not only how well each one works alone.

samspel-bench is a benchmark for agent-to-agent collaboration. *Samspel* is Swedish for interplay.

## Status

The harness runs end to end against Anthropic models through the Messages API, or against scripted mock agents with no API key. Three task families are implemented (see [Implemented](#implemented)); the rest of the design is listed under [Not yet implemented](#not-yet-implemented).

## Quick start

```sh
make build
bin/samspel list                                                        # families, controls, adversary scripts
bin/samspel run -config configs/mock-all.json -out results/mock -mock   # full harness self-test, no API key
export ANTHROPIC_API_KEY=...
bin/samspel run -config configs/smoke.json -out results/smoke           # small real run
bin/samspel run -config configs/core.json -out results/core -dry-run    # job count only
bin/samspel report -in results/core                                     # rebuild the report from results.jsonl
```

`ANTHROPIC_BASE_URL` overrides the endpoint (proxy or gateway). `ANTHROPIC_WORKSPACE_ID` names the workspace for a key that is not scoped to one. `-spool DIR` carries each run's messages through a JSONL file in DIR instead of memory; the file is what agents receive, so it can be read or tailed during a run. Ctrl-C stops cleanly. Rerunning the same command resumes: every job already in `results.jsonl` is skipped. A run that cannot write its results fails instead of reporting jobs done.

Exit status is 0 on success, 1 when a run fails, and 2 when the invocation is wrong (unknown subcommand or flag).

## Outputs

`results.jsonl` holds one line per run: success, score, family and common metrics, token usage (input, output, cache write, cache read), model calls, messages, message tokens, duration, error. `report.md` and `report.json` hold the scorecard. `traces/<job>.jsonl` (with `-traces`) holds every send, delivery, drop, duplicate, rejection, tool call, flag and model call of a run.

## Parallelism

Two levels. `parallel` concurrent runs, each running its agents as goroutines. All runs share one HTTP client and one limiter per model, so global traffic is bounded no matter how many runs are active.

The per-model limiter enforces `max_inflight` concurrent requests and token buckets for `rpm`, `itpm` (uncached input tokens per minute) and `otpm`. Output is reserved at `max_tokens` and settled against actual usage. A 429 sets a shared cooldown from `retry-after` for every caller of that model. 429, 529, 5xx and network errors retry with jittered exponential backoff; other 4xx fail the run, which is recorded with `error` and retried on the next resume.

Set `limits` and `model_limits` a little below your organization's limits (Console, Limits page). Rules of thumb: `parallel` of 2 to 4 times `max_inflight` keeps the request slots full, because agents spend time waiting on each other. Jobs are shuffled so models and families interleave.

The Message Batches API is not used: every agent turn depends on messages from the previous turns, so a run cannot be submitted as one batch.

## Prompt caching

Each request carries two cache breakpoints: the system prompt (which covers the tool definitions too) and the last block of the conversation. Each turn then reads the previous turn's prefix from cache. Cache reads and writes are recorded separately in usage.

## Cost

Tokens per run grow with turns, because each call resends the conversation. Check `report.md` token columns on `configs/smoke.json` before a large run, then scale. `configs/core.json` expands to 135,000 runs (3 models, 3 families, 10 conditions, 5 controls, 100 instances, 3 repeats). Use `-max-jobs` or fewer instances, conditions or repeats to stage it.

## Config

```json
{
  "models": ["claude-sonnet-5"],
  "seed": 1,
  "families": [{"name": "F1", "instances": 100, "n": 4, "params": {"suspects": 5}}],
  "conditions": [
    {"name": "core", "budget_tokens": 8000},
    {"name": "byz", "byzantine": {"count": 1, "script": "confident_wrong"}, "awareness": true},
    {"name": "faults", "channel": {"drop": 0.05, "duplicate": 0.05, "max_delay_ms": 2000, "lost_reply": 0.2}}
  ],
  "controls": ["team", "nocomm", "oracle", "broadcast_all", "ablation"],
  "repeats": 3,
  "parallel": 64,
  "agent": {"max_turns": 30, "max_tokens": 1024, "wait_timeout_sec": 20, "run_timeout_sec": 900},
  "limits": {"max_inflight": 32, "rpm": 1000, "itpm": 400000, "otpm": 80000},
  "model_limits": {"claude-opus-5-5": {"max_inflight": 16, "rpm": 500}}
}
```

Condition fields: `n` (honest team size), `topology` (`free` or `star`), `decision` (`single`: first submission ends the task; `all`: every honest agent submits), `awareness` (tell agents some peers may be adversarial), `budget_tokens` (per-agent total of sent message tokens), `max_message_tokens`, `channel` (`drop`, `duplicate`, `max_delay_ms` with reordering, `lost_reply` on side-effecting tools), `byzantine` (`count` adversaries added on top of the honest team, `script`).

Instance seeds depend only on `seed`, family and instance index, so controls, models, conditions and repeats are paired on the same instances. Honest agent IDs are a random subset of `agent-1..agent-(n+f)`, so adversaries cannot be spotted by number.

## Controls

| Control | Meaning |
| --- | --- |
| team | Honest agents plus any adversaries, communicating over the bus |
| nocomm | Same agents, no communication tools |
| oracle | One agent with every view and permission |
| broadcast_all | Every private view is delivered to every agent at the start; no further messaging |
| ablation | Messages are delivered with content replaced by length-matched noise |

G = (S_team - S_nocomm) / (S_oracle - S_nocomm), with a paired bootstrap 95% interval over instances.

## Implemented

Families:
- F1 information pooling. Hidden profile over 5 suspects. Three key clues held by three different agents; each clue and each pair leave at least two suspects, all three leave one.
- F5 relay. Chain topology, per-message token cap, 40-requirement specification, quiz at the end. Reports retention per hop.
- F8 ledger. Receivers know what they are owed, only payers can pay, overdrafts are rejected, transfers must apply exactly once. Reports exactly-once rate, double-applied, lost, stray, overdraft rejections, lost replies.

Adversary scripts: `confident_wrong`, `equivocator`, `injector` (canary-tagged prompt injection), `sycophant`, `silent`.

Common metrics: `info_recall` and `info_precision` (key clues shared), `contamination_rate` (honest agents repeating a planted false value or acting on the adversary's target), `injection_compromise` (canary in any honest message or tool input), `flag_precision`, `flag_recall`, `timed_out`.

Reference team: a minimal tool-use loop with `send`, `broadcast`, `wait`, `flag` and the family's task tools. `wait` returns early when every agent is idle, so teams do not deadlock on timeouts.

## Not yet implemented

- Families F2 (reference and grounding), F3 (division of labor), F4 (joint planning), F6 (discovery and routing), F7 (cross-checking).
- Conditions: authentication (signed or oral), timing (synchronous rounds), communication model (shared blackboard, pull), tree, gossip and independent-then-aggregate topologies.
- Adversary scripts: descriptor hijacker, relay forger. LLM-driven adversaries.
- The artifact store for resource references.
- Team track (plugging in an external protocol implementation) and protocol track.

## Caveats

- Marker matching for F1 and F5 metrics is approximate. It looks for the value and its context within a short window, so heavy paraphrase can be missed. Success and score never depend on markers; they come from the environment.
- The mock backend runs scripted policies that are deliberately naive (they trust every claim and never flag). Its purpose is to check the harness, not to be a baseline.
- Adversaries are added on top of the honest team, so the honest agents always hold the information needed to succeed.

## Layout

```
cmd/samspel          CLI: run, report, list, version
internal/llm         Messages API client, shared limiter, mock model
internal/bus         message bus: topology, budgets, drop/duplicate/delay, ablation, quiescence
internal/trace       append-only run trace
internal/task        families F1, F5, F8: generators, environments, verifiers, mock policies
internal/agent       reference agent loop and adversary scripts
internal/runner      config, job expansion, parallel pool, per-run execution, common metrics
internal/report      scorecard with bootstrap intervals
configs              smoke.json, core.json, mock-all.json
```

## Adding a family

Implement `task.Family` and `task.Instance` (generator, briefing, private views, oracle view, environment with task tools, verifier, markers, false claims, mock policy) and register it in `task.Registry`. The runner, bus, controls, adversaries and report pick it up without changes.

## Development

You need Go (the version is in `go.mod`) and [golangci-lint](https://golangci-lint.run) v2.5.0.

```sh
make build   # compile bin/samspel and point git at the repository's hooks
make test    # race-enabled tests with a cleared cache
make lint    # golangci-lint with the repository's config
make bench   # allocation benchmarks, results stay on your machine
make clean   # remove build and scratch output
```

The hooks in `.githooks/` refuse credentials, large files, binaries and unformatted Go at commit time, and run tests and lint before a push. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

Report a vulnerability by email, not in a public issue. See [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE)
