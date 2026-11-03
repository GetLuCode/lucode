// Command fakeclaude replays a recorded claude session; see internal/fakeclaude.
//
//	go build -o tmp/fakeclaude.exe ./cmd/fakeclaude
//	LUCODE_CLAUDE_BIN=<path>\tmp\fakeclaude.exe LUCODE_FAKE_CLAUDE_FIXTURE=<fixture.jsonl> lucode ...
package main

import "claude-orchestrator/internal/fakeclaude"

func main() { fakeclaude.Main() }
