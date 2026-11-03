// Package fakeclaude is a stand-in for the claude CLI that replays a recorded
// session, so lucode's executor, runners and UI can be exercised without spending
// tokens.
//
// The fixture is the raw stdout of a real `claude -p --output-format=stream-json
// --input-format=stream-json` run (see testdata/record.sh). Each time the fake
// receives a user message it replays the next recorded turn, a turn being the
// lines up to and including a `result`; once the recording runs out the last turn
// repeats. A `control_request` is answered with the recorded `control_response`.
// Session ids in the replayed lines are rewritten to the one lucode asked for.
//
// Point lucode at it with LUCODE_CLAUDE_BIN=<fakeclaude binary> and
// LUCODE_FAKE_CLAUDE_FIXTURE=<fixture.jsonl> (cmd/fakeclaude builds the binary).
package fakeclaude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvMarker makes a test binary behave as the fake claude (see fakeclaudetest).
	EnvMarker = "LUCODE_FAKE_CLAUDE"
	// EnvFixture is the path of the recorded session to replay.
	EnvFixture = "LUCODE_FAKE_CLAUDE_FIXTURE"
	// EnvDelay is an optional pause in milliseconds between replayed lines.
	EnvDelay = "LUCODE_FAKE_CLAUDE_DELAY_MS"
)

var (
	sessionRe = regexp.MustCompile(`"session_id":"([^"]+)"`)
	requestRe = regexp.MustCompile(`"request_id":"([^"]+)"`)
)

// Fixture is a recorded session split into turns.
type Fixture struct {
	Turns           [][]string // lines of each turn, ending at its result
	ControlResponse string     // the recorded reply to a control_request ("" if none)
	SessionID       string     // session id found in the recording
	ControlID       string     // request id of the recorded control_response
}

// Load reads a fixture file.
func Load(path string) (*Fixture, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fx := &Fixture{}
	var cur []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if fx.SessionID == "" {
			if m := sessionRe.FindStringSubmatch(line); m != nil {
				fx.SessionID = m[1]
			}
		}
		switch lineType(line) {
		case "control_response":
			if fx.ControlResponse == "" {
				fx.ControlResponse = line
				if m := requestRe.FindStringSubmatch(line); m != nil {
					fx.ControlID = m[1]
				}
			}
			continue // answered on request, not as part of a turn
		case "result":
			cur = append(cur, line)
			fx.Turns = append(fx.Turns, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(cur) > 0 {
		fx.Turns = append(fx.Turns, cur)
	}
	if len(fx.Turns) == 0 {
		return nil, fmt.Errorf("fixture %s has no turns", path)
	}
	return fx, nil
}

func lineType(line string) string {
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(line), &t)
	return t.Type
}

// Main runs the fake CLI: it reads stream-json from stdin and replays the fixture
// named by LUCODE_FAKE_CLAUDE_FIXTURE on stdout. It returns when stdin closes.
func Main() {
	path := os.Getenv(EnvFixture)
	if path == "" {
		fmt.Fprintln(os.Stderr, "fakeclaude: set", EnvFixture)
		os.Exit(2)
	}
	fx, err := Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeclaude:", err)
		os.Exit(2)
	}
	delay := time.Duration(0)
	if ms, err := strconv.Atoi(os.Getenv(EnvDelay)); err == nil {
		delay = time.Duration(ms) * time.Millisecond
	}
	sessID := sessionFromArgs(os.Args[1:])
	if sessID == "" {
		sessID = fx.SessionID
	}

	out := bufio.NewWriter(os.Stdout)
	emit := func(line string) {
		if fx.SessionID != "" {
			line = strings.ReplaceAll(line, fx.SessionID, sessID)
		}
		fmt.Fprintln(out, line)
		out.Flush()
		if delay > 0 {
			time.Sleep(delay)
		}
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 64<<20)
	turn := 0
	for in.Scan() {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "user":
			idx := turn
			if idx >= len(fx.Turns) {
				idx = len(fx.Turns) - 1
			}
			for _, l := range fx.Turns[idx] {
				emit(l)
			}
			turn++
		case "control_request":
			if fx.ControlResponse != "" {
				l := fx.ControlResponse
				if fx.ControlID != "" && msg.RequestID != "" {
					l = strings.ReplaceAll(l, fx.ControlID, msg.RequestID)
				}
				emit(l)
			}
		}
	}
}

// sessionFromArgs finds the session id lucode passed (--session-id or --resume).
func sessionFromArgs(args []string) string {
	for _, a := range args {
		for _, p := range []string{"--session-id=", "--resume="} {
			if strings.HasPrefix(a, p) {
				return strings.TrimPrefix(a, p)
			}
		}
	}
	return ""
}
