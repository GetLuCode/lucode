package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/taskmcp"
	"claude-orchestrator/internal/termrun"
)

// Command tools: an agent defines commands of the project, runs them and reads what they printed.
// Everything goes through the same paths as the Commands tab, so the process runs on the project's
// runner, never here; the hub only keeps the definition and asks the runner over RPC. Only commands
// of the agent's own project are reachable.

const (
	commandToolMax      = 24 * 1024 // longest answer
	commandWaitMax      = 300       // seconds a run may be waited for
	commandOutputLines  = 80
	commandOutputMax    = 500
	commandErrorsLimit  = 10
	commandErrorsMax    = 50
	commandTailOnFailed = 20
)

func (h *Handler) handleCommandTool(projectID, tool string, args json.RawMessage) (string, error) {
	p, err := dbpkg.GetProject(h.db, projectID)
	if err != nil {
		return "", fmt.Errorf("project not found: %w", err)
	}
	var a struct {
		Name        string         `json:"name"`
		Command     string         `json:"command"`
		Cwd         string         `json:"cwd"`
		RunnerID    string         `json:"runner_id"`
		Run         bool           `json:"run"`
		WaitSeconds float64        `json:"wait_seconds"`
		Lines       int            `json:"lines"`
		Limit       int            `json:"limit"`
		Shell       string         `json:"shell"`
		Env         []dbpkg.EnvVar `json:"env"`
		Path        string         `json:"path"`
		Args        []string       `json:"args"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("bad arguments: %w", err)
		}
	}
	switch tool {
	case taskmcp.ToolCommandList:
		return h.commandListTool(p)
	case taskmcp.ToolCommandCreate:
		return h.commandCreateTool(p, dbpkg.ProjectCommand{Name: a.Name, Command: a.Command, Cwd: a.Cwd, RunnerID: a.RunnerID, Shell: a.Shell, Env: a.Env}, a.Run)
	case taskmcp.ToolCommandShells:
		return h.commandShellsTool(p, a.RunnerID)
	case taskmcp.ToolCommandShellAdd:
		return h.commandShellAddTool(p, a.RunnerID, a.Name, a.Path, a.Args)
	case taskmcp.ToolCommandRun:
		return h.commandRunTool(p, a.Command, a.WaitSeconds)
	case taskmcp.ToolCommandStop:
		return h.commandStopTool(p, a.Command)
	case taskmcp.ToolCommandOutput:
		return h.commandOutputTool(p, a.Command, a.Lines)
	case taskmcp.ToolCommandErrors:
		return h.commandErrorsTool(p, a.Command, a.Limit)
	}
	return "", fmt.Errorf("unknown command tool %q", tool)
}

// findCommand resolves an id or a name (case-insensitive) among the project's commands.
func (h *Handler) findCommand(p dbpkg.Project, ref string) (dbpkg.ProjectCommand, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return dbpkg.ProjectCommand{}, errors.New("command is required: its id or name (see command_list)")
	}
	cmds, err := dbpkg.ListProjectCommands(h.db, p.ID)
	if err != nil {
		return dbpkg.ProjectCommand{}, err
	}
	var byName []dbpkg.ProjectCommand
	for _, c := range cmds {
		if c.ID == ref {
			return c, nil
		}
		if strings.EqualFold(c.Name, ref) {
			byName = append(byName, c)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return dbpkg.ProjectCommand{}, fmt.Errorf("no command %q in this project (see command_list)", ref)
	}
	return dbpkg.ProjectCommand{}, fmt.Errorf("%d commands are called %q: use the id (see command_list)", len(byName), ref)
}

// commandReplay is what the runner kept of a command: its output and state. ok is false when the
// runner has never run it (or was restarted since).
func (h *Handler) commandReplay(ctx context.Context, p dbpkg.Project, c dbpkg.ProjectCommand) (rep termReplay, ok bool, err error) {
	runner, _, err := h.commandRunner(p, c)
	if err != nil {
		return rep, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	code, err := h.runnerCall(ctx, runner, http.MethodPost, "/api/term/replay", map[string]string{"id": c.ID}, &rep)
	if err != nil {
		if code == http.StatusNotFound && strings.Contains(err.Error(), "not running") {
			return rep, false, nil
		}
		if code == http.StatusNotFound {
			return rep, false, fmt.Errorf("runner %s is older than the Commands tab: it has to be rebuilt and restarted", runner)
		}
		return rep, false, err
	}
	return rep, true, nil
}

func describeState(s termrun.Status) string {
	switch s.State {
	case termrun.StateRunning:
		return "running"
	case termrun.StateFailed:
		return "failed to start: " + s.Error
	}
	if s.ExitCode != nil && *s.ExitCode == 0 {
		return "finished (exit code 0)"
	}
	if s.ExitCode != nil {
		return fmt.Sprintf("exited with code %d", *s.ExitCode)
	}
	return "ended"
}

func (h *Handler) commandListTool(p dbpkg.Project) (string, error) {
	cmds, err := dbpkg.ListProjectCommands(h.db, p.ID)
	if err != nil {
		return "", err
	}
	if len(cmds) == 0 {
		return "This project has no commands yet. Define one with command_create.", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	states := h.commandStates(ctx, p, cmds)
	var b strings.Builder
	for _, c := range cmds {
		runner := c.RunnerID
		if runner == "" {
			runner = p.RunnerID
		}
		state := "not started (or its runner is unreachable)"
		if st, ok := states[runner][c.ID]; ok {
			state = describeState(st)
		}
		line := c.Command
		if line == "" {
			line = "(interactive shell)"
		}
		fmt.Fprintf(&b, "- %s  id=%s\n    command: %s\n    folder: %s\n    state: %s\n", c.Name, c.ID, line, orDot(c.Cwd), state)
		if c.Shell != "" {
			fmt.Fprintf(&b, "    shell: %s\n", c.Shell)
		}
		if len(c.Env) > 0 { // names only: a value may be a secret
			names := make([]string, len(c.Env))
			for i, v := range c.Env {
				names[i] = v.Name
			}
			fmt.Fprintf(&b, "    env: %s\n", strings.Join(names, ", "))
		}
	}
	return b.String(), nil
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

func (h *Handler) commandCreateTool(p dbpkg.Project, in dbpkg.ProjectCommand, run bool) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Cwd = strings.TrimSpace(in.Cwd)
	if strings.Trim(in.Cwd, `./\`) == "" {
		in.Cwd = ""
	}
	if in.Name == "" {
		return "", errors.New("name is required")
	}
	if err := checkRelDir(in.Cwd); err != nil {
		return "", err
	}
	if in.RunnerID != "" && !h.isRemoteRunner(in.RunnerID) {
		return "", errors.New("runner_id is not a runner")
	}
	in.Shell = strings.TrimSpace(in.Shell)
	if err := checkShellID(in.Shell); err != nil {
		return "", err
	}
	env, err := cleanEnv(in.Env)
	if err != nil {
		return "", err
	}
	in.Env = env
	if _, _, err := h.commandRunner(p, in); err != nil {
		return "", err // a command that could never run is not worth saving
	}
	if existing, err := dbpkg.ListProjectCommands(h.db, p.ID); err == nil {
		for _, c := range existing {
			if strings.EqualFold(c.Name, in.Name) {
				return "", fmt.Errorf("a command called %q already exists (id %s)", c.Name, c.ID)
			}
		}
	}
	in.ProjectID = p.ID
	saved, err := dbpkg.InsertProjectCommand(h.db, in)
	if err != nil {
		return "", err
	}
	h.notifyCommandsChanged(p.ID)
	msg := fmt.Sprintf("Command %q created (id %s).", saved.Name, saved.ID)
	if run {
		st, _, err := h.startCommandOn(context.Background(), p, saved, 0, 0)
		if err != nil {
			return msg + " It could not be started: " + err.Error(), nil
		}
		msg += fmt.Sprintf(" Started on the runner (pid %d).", st.PID)
	}
	return msg, nil
}

// shellList is what the runner answers to GET /api/term/shells.
type shellList struct {
	Shells []termrun.Shell `json:"shells"`
}

func describeShells(list []termrun.Shell) string {
	if len(list) == 0 {
		return "This runner offers no shells: commands use its default one."
	}
	var b strings.Builder
	for _, s := range list {
		origin := "registered"
		if s.Builtin {
			origin = "found by the runner"
		}
		fmt.Fprintf(&b, "- %s  id=%s  path=%s  (%s)\n", s.Name, s.ID, s.Path, origin)
	}
	return b.String()
}

// commandShellsTool asks the runner for its shells over RPC: the hub knows no shell paths.
func (h *Handler) commandShellsTool(p dbpkg.Project, runnerID string) (string, error) {
	runner, _, err := h.commandRunner(p, dbpkg.ProjectCommand{RunnerID: runnerID})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out shellList
	if code, err := h.runnerCall(ctx, runner, http.MethodGet, "/api/term/shells", nil, &out); err != nil {
		if code == http.StatusNotFound {
			return "", fmt.Errorf("runner %s is older than the shell option: rebuild and restart it", runner)
		}
		return "", err
	}
	return describeShells(out.Shells), nil
}

// commandShellAddTool registers a shell on a runner. The runner validates and keeps it; the user
// approves the call through the usual tool hook, because it names an executable on that machine.
func (h *Handler) commandShellAddTool(p dbpkg.Project, runnerID, name, path string, args []string) (string, error) {
	runner, _, err := h.commandRunner(p, dbpkg.ProjectCommand{RunnerID: runnerID})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var saved termrun.Shell
	body := map[string]any{"name": name, "path": path, "args": args}
	if _, err := h.runnerCall(ctx, runner, http.MethodPost, "/api/term/shells", body, &saved); err != nil {
		return "", err
	}
	return fmt.Sprintf("Shell %q registered on runner %s (id %s). Use that id as the shell of command_create.", saved.Name, runner, saved.ID), nil
}

func (h *Handler) commandRunTool(p dbpkg.Project, ref string, wait float64) (string, error) {
	c, err := h.findCommand(p, ref)
	if err != nil {
		return "", err
	}
	st, _, err := h.startCommandOn(context.Background(), p, c, 0, 0)
	if err != nil {
		return "", fmt.Errorf("%q: %w", c.Name, err)
	}
	if wait <= 0 {
		return fmt.Sprintf("%q started (pid %d). It keeps running: read it with command_output or command_errors, stop it with command_stop.", c.Name, st.PID), nil
	}
	limit := time.Duration(min(wait, commandWaitMax) * float64(time.Second))
	began := time.Now()
	for {
		rep, ok, err := h.commandReplay(context.Background(), p, c)
		if err != nil {
			return "", err
		}
		took := time.Since(began).Round(100 * time.Millisecond)
		if ok && rep.Status.Run == st.Run && rep.Status.State != termrun.StateRunning {
			return fmt.Sprintf("%q %s after %s.\n%s", c.Name, describeState(rep.Status), took, summarizeRun(rep, true)), nil
		}
		if time.Since(began) >= limit {
			text := "(no output yet)"
			if ok {
				text = summarizeRun(rep, false)
			}
			return fmt.Sprintf("%q is still running after %s. Last lines:\n%s", c.Name, took, text), nil
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// summarizeRun is what an agent needs after a run: the errors, and the tail when it went badly.
func summarizeRun(rep termReplay, ended bool) string {
	raw := string(rep.Data)
	blocks := termrun.FindErrors(raw, 5)
	failed := rep.Status.ExitCode != nil && *rep.Status.ExitCode != 0
	var b strings.Builder
	if len(blocks) > 0 {
		fmt.Fprintf(&b, "Errors found, most recent first:\n")
		for i, e := range blocks {
			fmt.Fprintf(&b, "%d. (line %d)\n%s\n", i+1, e.Line, e.Text)
		}
	} else if ended && !failed {
		b.WriteString("No errors found in the output.\n")
	}
	if failed || !ended {
		fmt.Fprintf(&b, "Last lines of output:\n%s\n", termrun.Tail(raw, commandTailOnFailed))
	}
	return b.String()
}

func (h *Handler) commandStopTool(p dbpkg.Project, ref string) (string, error) {
	c, err := h.findCommand(p, ref)
	if err != nil {
		return "", err
	}
	runner, _, err := h.commandRunner(p, c)
	if err != nil {
		return "", err
	}
	rep, ok, err := h.commandReplay(context.Background(), p, c)
	if err != nil {
		return "", err
	}
	if !ok || rep.Status.State != termrun.StateRunning {
		return fmt.Sprintf("%q is not running.", c.Name), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := h.runnerCall(ctx, runner, http.MethodPost, "/api/term/stop", map[string]string{"id": c.ID}, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("%q stopped.", c.Name), nil
}

func (h *Handler) commandOutputTool(p dbpkg.Project, ref string, lines int) (string, error) {
	c, err := h.findCommand(p, ref)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = commandOutputLines
	}
	lines = min(lines, commandOutputMax)
	rep, ok, err := h.commandReplay(context.Background(), p, c)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("%q has not run since its runner started: there is no output. Start it with command_run.", c.Name), nil
	}
	text := termrun.Tail(string(rep.Data), lines)
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	return capText(fmt.Sprintf("%q is %s. Last %d lines:\n%s", c.Name, describeState(rep.Status), lines, text)), nil
}

type commandErrorSet struct {
	cmd    dbpkg.ProjectCommand
	rep    termReplay
	blocks []termrun.ErrorBlock
	at     string // when it ended, or started
}

func (h *Handler) commandErrorsTool(p dbpkg.Project, ref string, limit int) (string, error) {
	if limit <= 0 {
		limit = commandErrorsLimit
	}
	limit = min(limit, commandErrorsMax)
	var cmds []dbpkg.ProjectCommand
	if strings.TrimSpace(ref) != "" {
		c, err := h.findCommand(p, ref)
		if err != nil {
			return "", err
		}
		cmds = []dbpkg.ProjectCommand{c}
	} else {
		all, err := dbpkg.ListProjectCommands(h.db, p.ID)
		if err != nil {
			return "", err
		}
		cmds = all
	}
	if len(cmds) == 0 {
		return "This project has no commands yet.", nil
	}
	var sets []commandErrorSet
	for _, c := range cmds {
		rep, ok, err := h.commandReplay(context.Background(), p, c)
		if err != nil {
			if len(cmds) == 1 {
				return "", err
			}
			continue // one unreachable runner must not hide the others
		}
		if !ok {
			continue
		}
		s := commandErrorSet{cmd: c, rep: rep, blocks: termrun.FindErrors(string(rep.Data), limit), at: rep.Status.StartedAt}
		if rep.Status.EndedAt != "" {
			s.at = rep.Status.EndedAt
		}
		sets = append(sets, s)
	}
	if len(sets) == 0 {
		return "None of the commands has run since its runner started, so there is nothing to read.", nil
	}
	sort.SliceStable(sets, func(i, j int) bool { return sets[i].at > sets[j].at }) // most recent command first

	var b strings.Builder
	left := limit
	for _, s := range sets {
		st := s.rep.Status
		fmt.Fprintf(&b, "## %s: %s", s.cmd.Name, describeState(st))
		if st.EndedAt != "" {
			fmt.Fprintf(&b, ", ended %s", st.EndedAt)
		} else if st.StartedAt != "" {
			fmt.Fprintf(&b, ", started %s", st.StartedAt)
		}
		b.WriteString("\n")
		shown := 0
		for _, e := range s.blocks {
			if left == 0 {
				break
			}
			shown++
			left--
			fmt.Fprintf(&b, "%d. (line %d of its output)\n%s\n", shown, e.Line, e.Text)
		}
		if shown == 0 {
			if st.ExitCode != nil && *st.ExitCode != 0 {
				fmt.Fprintf(&b, "No error text was recognised, but it exited with code %d. Last lines:\n%s\n", *st.ExitCode, termrun.Tail(string(s.rep.Data), 10))
			} else {
				b.WriteString("No errors found.\n")
			}
		}
		b.WriteString("\n")
	}
	return capText(b.String()), nil
}

func capText(s string) string {
	if len(s) <= commandToolMax {
		return s
	}
	cut := strings.LastIndexByte(s[:commandToolMax], '\n') + 1
	return s[:cut] + "[Cut here: ask for fewer lines or errors, or for one command.]\n"
}
