package session

import (
	"context"
	"os/exec"
)

type Status string

const (
	StatusIdle        Status = "idle"
	StatusRunning     Status = "running"
	StatusWaitingHook Status = "waiting_hook"
	StatusError       Status = "error"
)

type ProcessState struct {
	SessionID string
	Cmd       *exec.Cmd
	Done      chan struct{}
	Cancel    context.CancelFunc
}

type TurnOptions struct {
	SessionID       string
	ProjectID       string
	Message         string
	Model           string
	Name            string
	WorkDirs        []string
	ClaudeConfigDir string
	Resume          bool   // true → --resume=SessionID
	ForkFrom        string // non-empty → --fork-session --resume=ForkFrom --session-id=SessionID
	Effort          string // low|medium|high|max|auto
	Thinking        string // enabled|disabled|adaptive
	ImageBase64     string // base64-encoded image
	ImageMediaType  string // image/jpeg, image/png, etc.
}
