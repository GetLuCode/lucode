#!/usr/bin/env bash
# Records a real claude session as a fixture for the fake claude (internal/fakeclaude).
# Run it from anywhere; it spends a few haiku tokens. Usage: record.sh <output.jsonl>
# The two user messages and the context usage request are sent together, as lucode does.
set -euo pipefail
out="${1:?usage: record.sh <output.jsonl>}"
work="$(mktemp -d -p "$(dirname "$0")/../../../tmp" fixture-run.XXXXXX)"
sess="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || python -c 'import uuid;print(uuid.uuid4())')"
cd "$work"
printf '%s\n' \
  '{"type":"user","message":{"role":"user","content":"Remember the secret word PINEAPPLE. Reply with just: ok"}}' \
  '{"type":"user","message":{"role":"user","content":"What was the secret word? Answer with just the word."}}' \
  '{"type":"control_request","request_id":"cr-get_context_usage","request":{"subtype":"get_context_usage"}}' |
  claude -p --verbose --output-format=stream-json --include-hook-events --include-partial-messages \
    --input-format=stream-json --model=haiku --session-id="$sess" --name=Tester > "$out"
cd - >/dev/null
rm -rf "$work"
echo "recorded $(wc -l < "$out") lines to $out (session $sess)"
