package executor

import (
	"time"
)

// CommandEvent is the claude_command event: the exact command line a claude process
// was started with, where and as which process. It is published by the machine that
// runs claude (a runner has no other way to tell the hub what it launched), also when
// the start failed (err set, pid 0), because that is when the command matters most.
func CommandEvent(sessID, exe string, args []string, cwd string, pid int, startErr error) map[string]any {
	ev := map[string]any{
		"type":       "claude_command",
		"session_id": sessID,
		"exe":        exe,
		"args":       args,
		"cwd":        cwd,
		"pid":        pid,
		"at":         time.Now().UTC().Format(time.RFC3339Nano),
	}
	if startErr != nil {
		ev["error"] = startErr.Error()
	}
	return ev
}
