#!/bin/sh
# argv: workspace prompt ephemeral-home wall-seconds normalized-result final-output
set -eu
workspace=$1 prompt_file=$2 bench_home=$3 wall=$4 result=$5 final=$6 model=${7} reasoning=${8} variant=${9:-dsh-default-codex}
prompt=$(cat -- "$prompt_file")
export PYTHONDONTWRITEBYTECODE=1
raw=/tmp/dsh.final.md
raw_stderr=/tmp/dsh.stderr
trap 'rm -f "$raw" "$raw_stderr"' EXIT
mkdir -p "$bench_home" "$(dirname "$result")" "$(dirname "$final")"
# The runner seeds both the parent DSH OAuth record and Codex authentication
# into this ephemeral home. The adapter never writes or translates credentials.
export HOME="$bench_home" DSH_HOME="$bench_home/.dsh"
export BENCH_MODEL="$model"
export DSH_PERMISSION_MODE=workspace-write DSH_TELEMETRY_DISABLED=1
mkdir -p "$DSH_HOME"
cat >"$DSH_HOME/settings.yaml" <<EOF
agent-default-model:
  provider: openai-codex
  model: $model
  reasoningEffort: $reasoning
llm-pi-ai:
  providers:
    openai-codex:
      retryPolicy:
        mode: normal
        maxRetries: 0
EOF
start=$(date +%s.%N)
set +e
cd "$workspace"
# Headless DSH records a completed turn and prints its answer before the
# process finishes disposing long-lived provider handles. Run it in its own
# process group so the adapter can reap that bounded tail instead of waiting
# for the full wall clock whenever the one-shot runtime leaves an open handle.
patch=/opt/bench/dsh-default-codex.patch.yml
[ "$variant" = "dsh-modified-codex" ] && patch=/opt/bench/dsh-modified-codex.patch.yml
setsid timeout --foreground "$wall" dsh --profile headless --patch "$patch" "$prompt" >"$raw" 2>"$raw_stderr" &
runner_pid=$!
completed=0
deadline=$(( $(date +%s) + wall ))
while kill -0 "$runner_pid" 2>/dev/null; do
  if [ -d "$DSH_HOME/sessions" ] && grep -Rqs '"type":"turn/end".*"kind":"completed"' "$DSH_HOME/sessions"; then
    completed=1
    # Give the session writer a short grace period, then terminate the whole
    # process group. A successful turn is already durably recorded above.
    sleep 2
    kill -TERM -"$runner_pid" 2>/dev/null || true
    sleep 1
    kill -KILL -"$runner_pid" 2>/dev/null || true
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then break; fi
  sleep 0.2
done
wait "$runner_pid"
code=$?
if [ "$completed" -eq 1 ]; then code=0; fi
set -e
end=$(date +%s.%N)
# The runtime wrapper owns this temporary link. Remove it defensively even if
# the process-group escalation interrupted the wrapper's EXIT trap.
if [ -L "$workspace/node_modules" ] && [ "$(readlink "$workspace/node_modules")" = "/opt/dsh/apps/cli/node_modules" ]; then
  rm -f "$workspace/node_modules"
fi
python3 /opt/bench/normalize.py dsh "$raw" "$DSH_HOME/sessions" "$result" "$final" "$code" "$(awk "BEGIN {print $end-$start}")" "$raw_stderr" "$model" "$reasoning"
exit 0
