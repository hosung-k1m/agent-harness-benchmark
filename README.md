# Minimal Docker Coding-Agent Benchmark

This benchmark compares three agent-harness configurations using the same
copied fixture, prompt, resource limits, and unrestricted-egress MVP policy:

- **DeepSeek Harness default with Codex plugin** uses DSH's primary
  `llm-pi-ai/openai-codex` route and registers the Codex subagent provider in
  its normal dormant state.
- **DeepSeek Harness modified with Codex plugin** uses the same parent route
  and exposes the provider to the parent agent as `subagent_codex`.
- **Codex CLI** runs Codex directly without the DSH parent layer.

Every run records its selected model and reasoning level. The default remains
`gpt-5.6-luna` at `low` reasoning effort.

## Prerequisites

Install Go, Docker, and a current Codex CLI login. Build the two trusted
images before use:

```sh
docker build -f docker/agent.Dockerfile -t agent-harness-benchmark:latest .
docker build -f docker/verifier.Dockerfile -t agent-harness-verifier:latest .
go build -o bench ./cmd/bench
```

The agent image pins Codex CLI `0.151.0`, DSH commit
`0a53fb55bea101816fa226bb964ae2bed71c343b` (DSH `0.1.2-alpha.2`), Node
`22.19.0`, and pnpm `11.7.0`. The DSH variants need both an operator-created
DSH `openai-codex` OAuth credential for the parent route and a native Codex
subscription login for the Codex plugin. One login cannot be translated into
the other. Without both, DSH preflight reports `unsupported` with remediation
rather than falling back to another provider or model. Start the pinned DSH
Web UI, open **Settings → Models**, add **OpenAI Codex**, and complete its
supported OAuth login; the expected record key is `llm-pi-ai/openai-codex`.
Also run `codex login` for the native credential. The operator must assert that
both logins use the same subscription account. The benchmark stores no account
identifier or hash.

## Commands

```sh
./bench preflight --variant codex-cli
./bench preflight --variant dsh-default-codex
./bench preflight --variant dsh-modified-codex
./bench smoke --variant codex-cli
./bench smoke --variant dsh-default-codex
./bench smoke --variant dsh-modified-codex
./bench run --case slugify-v1 --variant codex-cli --trial 1
./bench run --case slugify-v1 --variant dsh-default-codex --trial 1
./bench run --case slugify-v1 --variant dsh-modified-codex --trial 1
./bench compare --case slugify-v1 --trials 3
```

## Web dashboard

Build the CLI, then start the local dashboard from the repository root:

```sh
go build -o bench ./cmd/bench
./bench serve
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Select one or more of the
three harnesses, a model, a compatible reasoning level, and one or more test
cases. Every selected harness × case pairing is queued as an independent
attempt; concurrent attempts receive separate containers, networks, homes,
and workspaces. Use `--workers N` to set the concurrency limit and `--addr` to
choose another listen address. The default is loopback-only.

The dashboard exposes these validated model/effort combinations:

- `gpt-6-astra`, `gpt-5.6-sol`, and `gpt-5.6-terra`: `low`, `medium`, `high`,
  `xhigh`, `max`, or `ultra`;
- `gpt-5.6-luna`: `low`, `medium`, `high`, `xhigh`, or `max`;
- `gpt-5.5` and `gpt-5.4-mini`: `low`, `medium`, `high`, or `xhigh`.

For DSH runs, the selection configures the DSH parent model. The bundled Codex
subagent provider accepts the selected model but does not expose a separate
per-run reasoning override for its child.

The live dashboard updates attempt status and elapsed time every second. Token
counts remain marked as pending until the provider emits its normalized usage;
they are never estimated. Completed batches are restored from
`.bench/batches/` when the server restarts.

Each DSH attempt also exports a bounded, credential-scanned `trajectory.json`
derived from DSH's durable session events. Open **View trajectory** to filter,
scrub, step through, or play the parent trajectory. Select any two completed
DSH attempts for the same case to compare their event sequences, timing,
usage, and outcomes side by side. The modified harness records the parent-side
Codex delegation lifecycle; the Codex plugin intentionally returns only the
child's final answer or safe diagnostic, so child reasoning and tool traffic
are not present in the parent trajectory.

Use **Import a case** to add a prompt plus one of:

- a `.zip`, `.tar`, `.tar.gz`, or `.tgz` repository archive;
- a repository folder selected in the browser; or
- a credential-free HTTPS Git URL.

Imported cases are staged and validated before being moved into
`cases/<case-id>/fixture`, with their prompt at `cases/<case-id>/prompt.txt`.
Archive traversal, links and special files, duplicate paths, oversized files,
and oversized imports are rejected. Imported cases do not gain trusted hidden
tests; their resulting workspace is exported without running the verifier. A
manually authored case with `hidden/` uses the trusted verifier just like
`slugify-v1`.

Preflight creates a disposable container and validates the explicit command or
profile selection, ephemeral credentials/home/session, noninteractive task,
and a nonce workspace edit. Smoke requires a current preflight and asks the
exact repository-summary prompt against `cases/smoke-repository`. Compare
requires fresh successful preflight and smoke states, randomizes all three
harness configurations serially within every trial, and records its seed and
actual order.

The coding prompt and visible fixture live in `cases/slugify-v1`; hidden tests
and protected digest data are only provided to the trusted verifier. The agent
image's build context excludes `cases/*/hidden`, and an agent container never
receives verifier code or hidden tests.

## Security and artifacts

Every attempt uses a fresh container, network, HOME, workspace and session.
Fixtures are copied into tmpfs-backed workspaces; they are never host-mounted.
The runner uses an unprivileged user, read-only root filesystem where Docker
permits it, dropped capabilities, no-new-privileges, PID/CPU/memory/tmpfs and
wall-time limits. It never mounts a Docker socket, host HOME, package cache,
or verifier code. Network access is intentionally the relaxed
`unrestricted-egress-v1` MVP policy.

Codex's `workspace-write` sandbox uses an unprivileged user namespace. The
agent container therefore sets `seccomp=unconfined` solely to permit that
namespace syscall under Docker; it remains unprivileged, has `cap-drop=ALL`,
and retains `no-new-privileges`, its read-only root, and all resource limits.

Only normalized agent JSON, final output, a bounded normalized DSH trajectory,
a sanitized workspace archive, verifier JSON, and bounded sanitized verifier
logs are retained. Raw streams, raw DSH sessions, credentials, HOME, and caches
stay in the container. Collection is rejected if one of the exact seeded
sensitive values is found.

Artifacts are written beneath `.bench/runs/attempt-*`: `result.json` is the
host-normalized result, `out/final.md` is the final agent response, DSH runs
contain `out/trajectory.json`, coding runs also contain `workspace.tar`, and
`verifier/result.json` contains the bounded verifier outcome/logs. Compare
plans and rendered summaries live beneath `.bench/compare/`; each plan records
its seed, requested order, actual completed attempt order, and paths to the
stored result artifacts used for the summary.

The verifier rejects absolute/traversal paths, links, devices, sockets, FIFOs,
oversized archives, and protected README/test changes before running visible
and hidden Python tests. Its entry point is `docker/verifier/verify.py` and it
accepts an archive, hidden-assets directory, and output path.

## Token caveats and troubleshooting

Token values are provider-reported only. Missing values are JSON `null`, never
zero or an estimate. Codex uses its JSONL terminal usage event. DSH sums only
persisted `assistant/chunk` usage events and derives total input from the
provider-reported `totalTokens - outputTokens`, which includes cached input.
Any absent cache value makes cached input `null`. DSH's reasoning-token mapping
is unavailable and is always `null`. Tables print unavailable medians as `—`.

Quota or rate-limit failures are ordinary failed trials and are never retried.
If a preflight turns stale after a manifest, image, adapter, model, or effort
change, rerun preflight and smoke. Docker cleanup is attempted on success,
errors, and timeouts; inspect `docker ps -a` and `docker network ls` if a host
daemon crash interrupts cleanup.
