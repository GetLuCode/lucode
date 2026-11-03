# fakeclaude

Replays a recorded `claude -p` session so lucode can be tested without spending tokens.

- `testdata/two_turns.jsonl`: real haiku session, two turns (remember a word, repeat it) plus the context usage reply.
- `testdata/usage_limit.jsonl`: real run rejected by the 5-hour usage limit (`is_error`, 429).
- `testdata/record.sh <out.jsonl>`: records a new fixture with the real CLI (spends a few tokens).

In Go tests: `fakeclaudetest.Intercept()` in `TestMain`, then `fakeclaudetest.Use(t, "two_turns.jsonl")`.

In the running app (agents then use the recording instead of claude):

    go build -o tmp/fakeclaude.exe ./cmd/fakeclaude
    set LUCODE_CLAUDE_BIN=E:\dev\lucode\tmp\fakeclaude.exe
    set LUCODE_FAKE_CLAUDE_FIXTURE=E:\dev\lucode\internal\fakeclaude\testdata\two_turns.jsonl
    (start lucode / air from the same shell)

Each user message replays the next recorded turn; after the last one the last turn repeats.
`LUCODE_FAKE_CLAUDE_DELAY_MS` slows the replay down to watch the UI stream.
