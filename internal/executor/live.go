package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"sync"
	"time"
)

// LiveIdleTimeout is how long a live process waits for its next message before
// it exits on its own. The conversation is not lost: the next message starts a
// new process with --resume.
const LiveIdleTimeout = 30 * time.Minute

var (
	// ErrLiveBusy is returned by Send while a turn is still running.
	ErrLiveBusy = errors.New("live process is busy with a turn")
	// ErrLiveGone is returned by Send once the process has exited.
	ErrLiveGone = errors.New("live process has exited")
)

// Live is a long-running `claude -p --input-format stream-json` process that
// serves many turns for one session. Unlike Run, which starts a process per turn
// and closes it afterwards, a Live keeps the conversation in memory: later
// messages are written to its stdin, so there is no process start, no --resume
// and no MCP server restart per turn. Events go to publish for the whole life of
// the process.
type Live struct {
	req     TurnRequest
	ps      *ProcessState
	publish func(map[string]any)
	onExit  func()
	cancel  context.CancelFunc

	mu       sync.Mutex
	turnDone chan struct{} // closed when the turn in flight ends; nil while idle
	idle     *time.Timer
	exited   bool
}

// StartLive spawns the process and sends req.Message as its first message. The
// returned channel closes when that first turn ends (or the process dies).
// onExit is called once, after the process is gone.
func StartLive(req TurnRequest, publish func(map[string]any), onExit func()) (*Live, <-chan struct{}, error) {
	ctx, cancel := context.WithCancel(context.Background())
	sr, err := spawnClaude(ctx, req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	l := &Live{
		req:      req,
		ps:       sr.ps,
		publish:  publish,
		onExit:   onExit,
		cancel:   cancel,
		turnDone: make(chan struct{}),
	}
	first := l.turnDone
	go l.loop(sr.stdout)
	return l, first, nil
}

// PID is the operating-system id of the claude process (0 if unknown).
func (l *Live) PID() int {
	if l.ps == nil || l.ps.Cmd == nil || l.ps.Cmd.Process == nil {
		return 0
	}
	return l.ps.Cmd.Process.Pid
}

// Alive reports whether the process can still take messages.
func (l *Live) Alive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.exited
}

// Send writes a user message to the live process and returns a channel that
// closes when the turn ends. It fails with ErrLiveBusy while another turn runs.
func (l *Live) Send(text, imageBase64, imageMediaType string) (<-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exited {
		return nil, ErrLiveGone
	}
	if l.turnDone != nil {
		return nil, ErrLiveBusy
	}
	if l.idle != nil {
		l.idle.Stop()
	}

	done := make(chan struct{})
	l.turnDone = done // before the write, so a fast result cannot be missed
	line := userMessageLine(text, imageBase64, imageMediaType)

	l.ps.StdinMu.Lock()
	var err error
	if l.ps.StdinPipe == nil {
		err = ErrLiveGone
	} else {
		_, err = fmt.Fprintf(l.ps.StdinPipe, "%s\n", line)
	}
	l.ps.StdinMu.Unlock()
	if err != nil {
		l.turnDone = nil
		return nil, err
	}
	return done, nil
}

// Close asks the process to exit by closing its stdin.
func (l *Live) Close() { closeStdin(l.ps) }

// Kill stops the process immediately (used to cancel a turn in flight).
func (l *Live) Kill() { l.cancel() }

// endTurn marks the turn finished, arms the idle timer and wakes the waiter.
func (l *Live) endTurn() {
	l.mu.Lock()
	done := l.turnDone
	l.turnDone = nil
	if !l.exited {
		if l.idle != nil {
			l.idle.Stop()
		}
		l.idle = time.AfterFunc(LiveIdleTimeout, l.Close)
	}
	l.mu.Unlock()
	if done != nil {
		close(done)
	}
}

// loop reads the process output for its whole life, turn after turn.
func (l *Live) loop(stdout io.Reader) {
	sessID := l.req.SessionID
	sid8 := sessID[:8]
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	accum := newTurnAccum()
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		log.Printf("[executor/stdout] session=%s: %s", sid8, string(line))

		event, isResult, err := ParseLine(sessID, line)
		if err != nil {
			log.Printf("[executor] session=%s parse error: %v", sid8, err)
			continue
		}
		if event.EventType == "" {
			continue
		}

		if event.EventType == "control_response" {
			if cu, err := ParseContextUsage(event.Event); err == nil {
				l.publish(map[string]any{
					"type":        "context_usage",
					"session_id":  sessID,
					"categories":  cu.Categories,
					"totalTokens": cu.TotalTokens,
					"maxTokens":   cu.MaxTokens,
					"percentage":  cu.Percentage,
					"model":       cu.Model,
				})
			}
			continue // a live process stays open: nothing to close here
		}

		if isResult && resumeFailed(l.req, event, accum) {
			publishResumeFailure(l.req, l.publish)
			l.endTurn()
			continue // claude exits on its own; the exit path below cleans up
		}

		if event.EventType == "rate_limit" && event.LimitRejected {
			accum.resetsAt = event.ResetsAt
		}
		if isResult && turnAPIError(event) {
			publishTurnAPIError(sessID, event, accum.resetsAt, l.publish)
			accum = newTurnAccum()
			l.endTurn()
			continue
		}

		l.publish(parsedEventToMap(event))

		if event.EventType == "stream_event" && event.Event != nil {
			for _, msg := range accum.process(event.Event, sessID) {
				l.publish(map[string]any{
					"type":       "message_ready",
					"session_id": sessID,
					"id":         msg.id,
					"role":       msg.role,
					"content":    msg.content,
				})
			}
		}

		if isResult {
			log.Printf("[executor] session=%s live turn complete: subtype=%s", sid8, event.ResultSubtype)
			if accum.inputTokens > 0 || accum.outputTokens > 0 {
				l.publish(map[string]any{
					"type":                        "turn_usage",
					"session_id":                  sessID,
					"project_id":                  l.req.ProjectID,
					"account_id":                  l.req.AccountID,
					"model":                       l.req.Model,
					"input_tokens":                accum.inputTokens,
					"output_tokens":               accum.outputTokens,
					"cache_creation_input_tokens": accum.cacheCreationInputTokens,
					"cache_read_input_tokens":     accum.cacheReadInputTokens,
				})
			}
			l.publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "idle"})
			accum = newTurnAccum()
			l.endTurn()
			if err := RequestContextUsage(l.ps); err != nil {
				log.Printf("[executor] session=%s context request error: %v", sid8, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[executor] session=%s scanner error: %v", sid8, err)
	}

	// The process is gone.
	l.ps.Cmd.Wait()
	exitCode := 0
	if l.ps.Cmd.ProcessState != nil {
		exitCode = l.ps.Cmd.ProcessState.ExitCode()
	}
	log.Printf("[executor] session=%s live process exited code=%d", sid8, exitCode)

	l.mu.Lock()
	l.exited = true
	inFlight := l.turnDone != nil
	if l.idle != nil {
		l.idle.Stop()
	}
	l.mu.Unlock()

	if inFlight {
		if exitCode != 0 {
			l.publish(map[string]any{"type": "error", "session_id": sessID, "message": "claude exited with code " + strconv.Itoa(exitCode)})
			l.publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "error"})
		} else {
			l.publish(map[string]any{"type": "session_status", "session_id": sessID, "status": "idle"})
		}
		l.endTurn()
	}
	closeStdin(l.ps)
	l.cancel()
	close(l.ps.Done)
	if l.onExit != nil {
		l.onExit()
	}
}
