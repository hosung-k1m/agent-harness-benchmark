#!/bin/sh
# argv: workspace prompt ephemeral-home wall-seconds normalized-result final-output
set -eu
workspace=$1 prompt_file=$2 bench_home=$3 wall=$4 result=$5 final=$6
prompt=$(cat -- "$prompt_file")
export PYTHONDONTWRITEBYTECODE=1
raw=/tmp/codex.raw.jsonl
raw_stderr=/tmp/codex.stderr
trap 'rm -f "$raw" "$raw_stderr"' EXIT
mkdir -p "$bench_home" "$(dirname "$result")" "$(dirname "$final")"
start=$(date +%s.%N)
set +e
HOME="$bench_home" timeout --foreground "$wall" codex exec --json --ephemeral --skip-git-repo-check \
  --ignore-user-config --ignore-rules --strict-config --model gpt-5.6-luna \
  --sandbox workspace-write --output-last-message "$final" \
  --config 'approval_policy="never"' --config 'model_reasoning_effort="low"' \
  --config 'sandbox_workspace_write.network_access=true' -C "$workspace" "$prompt" >"$raw" 2>"$raw_stderr"
code=$?
set -e
end=$(date +%s.%N)
python3 /opt/bench/normalize.py codex "$raw" "$bench_home/.dsh/sessions" "$result" "$final" "$code" "$(awk "BEGIN {print $end-$start}")" "$raw_stderr"
exit 0
