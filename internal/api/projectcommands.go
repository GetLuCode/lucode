package api

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/termrun"
)

// The Commands tab, hub side. The hub keeps the definitions (name, command line, folder) and shows
// state; the processes run in a pseudo-terminal on a RUNNER (termrunner.go) and the hub only asks it
// over the RPC bridge and relays the terminal's bytes between its NATS subjects and the browser.

// commandView is a definition plus what the runner says about it.
type commandView struct {
	dbpkg.ProjectCommand
	Runner string          `json:"runner"` // the runner it runs on (RunnerID, or the project's)
	Status *termrun.Status `json:"status,omitempty"`
}

type commandList struct {
	Commands []commandView   `json:"commands"`
	Targets  []gitRepoTarget `json:"targets"` // the runners the project has a folder on
	Reason   string          `json:"reason,omitempty"`
}

// commandRunner is the runner a command runs on and the project's work dir there.
func (h *Handler) commandRunner(p dbpkg.Project, c dbpkg.ProjectCommand) (runner, base string, err error) {
	runner = c.RunnerID
	if runner == "" {
		runner = p.RunnerID
	}
	if !h.isRemoteRunner(runner) {
		return "", "", errors.New("this project has no runner, and only a runner can run commands. Choose one in the project's Environment")
	}
	dirs := p.EnvDirs[runner]
	if len(dirs) == 0 {
		return "", "", errors.New("this project has no work directory on that runner. Set one in the project's Environment")
	}
	return runner, dirs[0], nil
}

const (
	maxEnvVars     = 64
	maxEnvNameLen  = 128
	maxEnvValueLen = 8192
)

// cleanEnv checks the environment variables of a command and drops the empty rows. A name is letters,
// digits and "_" and does not start with a digit, which every shell and OS accepts; names are unique
// (without regard to case, because Windows ignores it).
func cleanEnv(in []dbpkg.EnvVar) ([]dbpkg.EnvVar, error) {
	out := make([]dbpkg.EnvVar, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		name := strings.TrimSpace(v.Name)
		if name == "" && v.Value == "" {
			continue
		}
		if name == "" {
			return nil, errors.New("an environment variable has a value but no name")
		}
		if len(name) > maxEnvNameLen || !envNameRe.MatchString(name) {
			return nil, fmt.Errorf("%q is not a valid variable name: use letters, digits and _, and do not start with a digit", name)
		}
		if strings.ContainsRune(v.Value, 0) || len(v.Value) > maxEnvValueLen {
			return nil, fmt.Errorf("the value of %s is too long (max %d characters) or has a NUL character", name, maxEnvValueLen)
		}
		key := strings.ToUpper(name)
		if seen[key] {
			return nil, fmt.Errorf("the variable %s is set twice", name)
		}
		seen[key] = true
		out = append(out, dbpkg.EnvVar{Name: name, Value: v.Value})
	}
	if len(out) > maxEnvVars {
		return nil, fmt.Errorf("too many environment variables (max %d)", maxEnvVars)
	}
	return out, nil
}

// checkShellID only checks the shape of a shell id; the runner knows whether it has that shell.
func checkShellID(id string) error {
	if len(id) > 64 || strings.ContainsAny(id, " \t\r\n/\\") {
		return errors.New("that is not a shell id (take it from the runner's list)")
	}
	return nil
}

var envNameRe =regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkRelDir keeps the folder of a command relative and inside the work dir.
func checkRelDir(rel string) error {
	if rel == "" {
		return nil
	}
	slash := filepath.ToSlash(rel)
	if filepath.IsAbs(rel) || strings.HasPrefix(slash, "/") || filepath.VolumeName(rel) != "" {
		return errors.New("the folder must be relative to the project's work directory")
	}
	for _, part := range strings.Split(slash, "/") {
		if part == ".." {
			return errors.New("the folder must stay inside the project's work directory")
		}
	}
	return nil
}

func (h *Handler) registerCommands(mux *http.ServeMux) {
	h.watchTerminals()
	mux.HandleFunc("GET /api/projects/{pid}/commands", h.listCommandsAPI)
	mux.HandleFunc("GET /api/projects/{pid}/command-shells", h.listCommandShells)
	mux.HandleFunc("POST /api/projects/{pid}/command-shells", h.addCommandShell)
	mux.HandleFunc("DELETE /api/projects/{pid}/command-shells/{sid}", h.deleteCommandShell)
	mux.HandleFunc("POST /api/projects/{pid}/commands", h.saveCommand)
	mux.HandleFunc("PUT /api/projects/{pid}/commands/{cid}", h.saveCommand)
	mux.HandleFunc("DELETE /api/projects/{pid}/commands/{cid}", h.deleteCommand)
	mux.HandleFunc("POST /api/projects/{pid}/commands/{cid}/start", h.startCommand)
	mux.HandleFunc("POST /api/projects/{pid}/commands/{cid}/stop", h.stopCommand)
	mux.HandleFunc("POST /api/projects/{pid}/commands/{cid}/ticket", h.commandTicket)
	// outside /api/: a browser cannot send the token with a WebSocket, so it carries a ticket
	mux.HandleFunc("GET /ws/term", h.termSocket)
}

// notifyCommandsChanged tells the open Commands tabs of the project to reload their list.
func (h *Handler) notifyCommandsChanged(projectID string) {
	if h.bus == nil || h.bus.OnBroadcast == nil {
		return
	}
	ev, _ := json.Marshal(map[string]any{"type": "commands_changed", "project_id": projectID})
	h.bus.OnBroadcast(ev)
}

// ownedCommand loads a command and checks it belongs to the project in the path.
func (h *Handler) ownedCommand(w http.ResponseWriter, r *http.Request) (dbpkg.Project, dbpkg.ProjectCommand, bool) {
	p, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return p, dbpkg.ProjectCommand{}, false
	}
	c, err := dbpkg.GetProjectCommand(h.db, r.PathValue("cid"))
	if err != nil || c.ProjectID != p.ID {
		http.Error(w, "command not found", http.StatusNotFound)
		return p, c, false
	}
	return p, c, true
}

func (h *Handler) listCommandsAPI(w http.ResponseWriter, r *http.Request) {
	p, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	cmds, err := dbpkg.ListProjectCommands(h.db, p.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	targets := h.projectGitTargets(p)
	res := commandList{Commands: make([]commandView, 0, len(cmds)), Targets: targets.Targets, Reason: targets.Reason}

	states := h.commandStates(r.Context(), p, cmds)
	for _, c := range cmds {
		v := commandView{ProjectCommand: c, Runner: c.RunnerID}
		if v.Runner == "" {
			v.Runner = p.RunnerID
		}
		if st, ok := states[v.Runner][c.ID]; ok {
			s := st
			v.Status = &s
		}
		res.Commands = append(res.Commands, v)
	}
	jsonResp(w, http.StatusOK, res)
}

// commandStates asks each runner of the commands what it says about its processes: once per
// runner, in parallel. A runner that is offline, or older than this tab, has no entry.
func (h *Handler) commandStates(ctx context.Context, p dbpkg.Project, cmds []dbpkg.ProjectCommand) map[string]map[string]termrun.Status {
	states := map[string]map[string]termrun.Status{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range cmds {
		runner := c.RunnerID
		if runner == "" {
			runner = p.RunnerID
		}
		if !h.isRemoteRunner(runner) {
			continue
		}
		mu.Lock()
		_, asked := states[runner]
		if !asked {
			states[runner] = nil
		}
		mu.Unlock()
		if asked {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			var list []termrun.Status
			if code, err := h.runnerCall(ctx, runner, http.MethodGet, "/api/term/list", nil, &list); err != nil || code != http.StatusOK {
				return
			}
			m := map[string]termrun.Status{}
			for _, s := range list {
				m[s.ID] = s
			}
			mu.Lock()
			states[runner] = m
			mu.Unlock()
		}()
	}
	wg.Wait()
	return states
}

func (h *Handler) saveCommand(w http.ResponseWriter, r *http.Request) {
	p, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	var in dbpkg.ProjectCommand
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Cwd = strings.TrimSpace(in.Cwd)
	if strings.Trim(in.Cwd, `./\`) == "" {
		in.Cwd = ""
	}
	if in.Name == "" {
		http.Error(w, "give the command a name", http.StatusBadRequest)
		return
	}
	if err := checkRelDir(in.Cwd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.RunnerID != "" && !h.isRemoteRunner(in.RunnerID) {
		http.Error(w, "that is not a runner", http.StatusBadRequest)
		return
	}
	in.Shell = strings.TrimSpace(in.Shell)
	if err := checkShellID(in.Shell); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	envGiven := in.Env != nil // an older page that does not send "env" must not wipe it
	if in.Env, err = cleanEnv(in.Env); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	in.ProjectID = p.ID
	if cid := r.PathValue("cid"); cid != "" {
		old, err := dbpkg.GetProjectCommand(h.db, cid)
		if err != nil || old.ProjectID != p.ID {
			http.Error(w, "command not found", http.StatusNotFound)
			return
		}
		in.ID = cid
		if !envGiven {
			in.Env = old.Env
		}
		if err := dbpkg.UpdateProjectCommand(h.db, in); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		in.CreatedAt = old.CreatedAt
	} else {
		if in, err = dbpkg.InsertProjectCommand(h.db, in); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	h.notifyCommandsChanged(p.ID)
	jsonResp(w, http.StatusOK, in)
}

// shellRunner is the runner whose shells are asked about: ?runner=, else the project's.
func (h *Handler) shellRunner(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return "", false
	}
	runner, _, err := h.commandRunner(p, dbpkg.ProjectCommand{RunnerID: r.URL.Query().Get("runner")})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return "", false
	}
	return runner, true
}

// forwardShells passes a shell order to the runner and answers what it said. The shells, and the
// list of them, belong to the runner: the hub stores no path and runs nothing.
func (h *Handler) forwardShells(w http.ResponseWriter, r *http.Request, method, path string, in any) {
	runner, ok := h.shellRunner(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var out json.RawMessage
	code, err := h.runnerCall(ctx, runner, method, path, in, &out)
	if err != nil {
		if code == http.StatusNotFound && method == http.MethodGet {
			code, err = http.StatusConflict, fmt.Errorf("runner %s is older than the shell option: rebuild and restart it", runner)
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (h *Handler) listCommandShells(w http.ResponseWriter, r *http.Request) {
	h.forwardShells(w, r, http.MethodGet, "/api/term/shells", nil)
}

func (h *Handler) addCommandShell(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string   `json:"name"`
		Path string   `json:"path"`
		Args []string `json:"args"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.forwardShells(w, r, http.MethodPost, "/api/term/shells", in)
}

func (h *Handler) deleteCommandShell(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	if sid == "" || len(sid) > 64 || strings.ContainsAny(sid, "/ ?#") {
		http.Error(w, "bad shell id", http.StatusBadRequest)
		return
	}
	h.forwardShells(w, r, http.MethodDelete, "/api/term/shells/"+sid, nil)
}

func (h *Handler) deleteCommand(w http.ResponseWriter, r *http.Request) {
	p, c, ok := h.ownedCommand(w, r)
	if !ok {
		return
	}
	if runner, _, err := h.commandRunner(p, c); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		_, _ = h.runnerCall(ctx, runner, http.MethodPost, "/api/term/stop", map[string]string{"id": c.ID}, nil)
		_, _ = h.runnerCall(ctx, runner, http.MethodPost, "/api/term/forget", map[string]string{"id": c.ID}, nil)
	}
	if err := dbpkg.DeleteProjectCommand(h.db, c.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.notifyCommandsChanged(p.ID)
	w.WriteHeader(http.StatusNoContent)
}

// startCommandOn starts a command on its runner and answers the runner's status. The int is the HTTP
// status to give when it fails.
func (h *Handler) startCommandOn(ctx context.Context, p dbpkg.Project, c dbpkg.ProjectCommand, cols, rows int) (termrun.Status, int, error) {
	runner, base, err := h.commandRunner(p, c)
	if err != nil {
		return termrun.Status{}, http.StatusUnprocessableEntity, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var st termrun.Status
	code, err := h.runnerCall(ctx, runner, http.MethodPost, "/api/term/start", termStartReq{
		ID: c.ID, Base: base, Cwd: c.Cwd, Command: c.Command, Env: c.Env, Shell: c.Shell, Cols: cols, Rows: rows,
	}, &st)
	if err != nil {
		if code == http.StatusNotFound {
			code = http.StatusConflict
			err = fmt.Errorf("runner %s is older than the Commands tab: rebuild and restart it", runner)
		}
		return termrun.Status{}, code, err
	}
	return st, http.StatusOK, nil
}

func (h *Handler) startCommand(w http.ResponseWriter, r *http.Request) {
	p, c, ok := h.ownedCommand(w, r)
	if !ok {
		return
	}
	var size struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&size)
	st, code, err := h.startCommandOn(r.Context(), p, c, size.Cols, size.Rows)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	jsonResp(w, http.StatusOK, st)
}

func (h *Handler) stopCommand(w http.ResponseWriter, r *http.Request) {
	p, c, ok := h.ownedCommand(w, r)
	if !ok {
		return
	}
	runner, _, err := h.commandRunner(p, c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if code, err := h.runnerCall(ctx, runner, http.MethodPost, "/api/term/stop", map[string]string{"id": c.ID}, nil); err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- state: the runner reports every change, the hub passes it to the browsers ----

func (h *Handler) watchTerminals() {
	if h.bus == nil {
		return
	}
	h.term.watch.Do(func() {
		_, err := h.bus.SubscribeRaw(natspkg.TermStatusSubject, func(_ string, data []byte) {
			var st termrun.Status
			if json.Unmarshal(data, &st) != nil {
				return
			}
			c, err := dbpkg.GetProjectCommand(h.db, st.ID)
			if err != nil {
				return // a process of another hub, or of a command already deleted
			}
			ev, _ := json.Marshal(map[string]any{"type": "command_status", "project_id": c.ProjectID, "command_id": c.ID, "status": st})
			if h.bus.OnBroadcast != nil {
				h.bus.OnBroadcast(ev)
			}
		})
		if err != nil {
			log.Printf("[commands] watch terminals: %v", err)
		}
	})
}

// ---- the terminal socket ----

type termTicket struct {
	cmd     string
	project string
	expires time.Time
}

var (
	termTicketsMu sync.Mutex
	termTickets   = map[string]termTicket{}
)

func (h *Handler) commandTicket(w http.ResponseWriter, r *http.Request) {
	p, c, ok := h.ownedCommand(w, r)
	if !ok {
		return
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		http.Error(w, "no randomness", http.StatusInternalServerError)
		return
	}
	t := hex.EncodeToString(raw[:])
	now := time.Now()
	termTicketsMu.Lock()
	for k, v := range termTickets {
		if now.After(v.expires) {
			delete(termTickets, k)
		}
	}
	termTickets[t] = termTicket{cmd: c.ID, project: p.ID, expires: now.Add(30 * time.Second)}
	termTicketsMu.Unlock()
	jsonResp(w, http.StatusOK, map[string]string{"ticket": t})
}

// redeemTermTicket works once.
func redeemTermTicket(t string) (termTicket, bool) {
	termTicketsMu.Lock()
	defer termTicketsMu.Unlock()
	v, ok := termTickets[t]
	delete(termTickets, t)
	return v, ok && time.Now().Before(v.expires)
}

// termSocket joins a browser to a command's terminal. Protocol of GET /ws/term?ticket=...:
//
//	server -> binary                       terminal output
//	server -> {"type":"reset"}             the process was started again: clear the screen
//	server -> {"type":"error","message"}   the terminal cannot be reached
//	client -> binary                       keystrokes
//	client -> {"type":"resize","cols":..,"rows":..}
func (h *Handler) termSocket(w http.ResponseWriter, r *http.Request) {
	tk, ok := redeemTermTicket(r.URL.Query().Get("ticket"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p, err := dbpkg.GetProject(h.db, tk.project)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	c, err := dbpkg.GetProjectCommand(h.db, tk.cmd)
	if err != nil {
		http.Error(w, "command not found", http.StatusNotFound)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var wmu sync.Mutex
	send := func(kind int, data []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.WriteMessage(kind, data)
	}
	fail := func(msg string) {
		b, _ := json.Marshal(map[string]string{"type": "error", "message": msg})
		send(websocket.TextMessage, b)
	}
	runner, _, err := h.commandRunner(p, c)
	if err != nil {
		fail(err.Error())
		return
	}

	// Live output is subscribed before the replay is asked for, and the run number and offsets cut
	// the overlap, so nothing is missed or shown twice between the two.
	type chunk struct {
		run, off uint64
		data     []byte
	}
	var (
		mu      sync.Mutex
		ready   bool
		pending []chunk
		curRun  uint64
		sentEnd uint64
	)
	deliver := func(ch chunk) { // with mu held
		if ch.run != curRun {
			if ch.run < curRun {
				return // a run that already ended
			}
			send(websocket.TextMessage, []byte(`{"type":"reset"}`))
			curRun, sentEnd = ch.run, 0
		}
		if ch.off+uint64(len(ch.data)) <= sentEnd {
			return
		}
		if ch.off < sentEnd {
			ch.data, ch.off = ch.data[sentEnd-ch.off:], sentEnd
		}
		send(websocket.BinaryMessage, ch.data)
		sentEnd = ch.off + uint64(len(ch.data))
	}
	unsub, err := h.bus.SubscribeRaw(natspkg.TermOutSubject(c.ID), func(_ string, d []byte) {
		if len(d) < 16 {
			return
		}
		ch := chunk{run: binary.BigEndian.Uint64(d), off: binary.BigEndian.Uint64(d[8:]), data: append([]byte(nil), d[16:]...)}
		mu.Lock()
		defer mu.Unlock()
		if !ready {
			pending = append(pending, ch)
			return
		}
		deliver(ch)
	})
	if err != nil {
		fail(err.Error())
		return
	}
	defer unsub()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	var rep termReplay
	code, rerr := h.runnerCall(ctx, runner, http.MethodPost, "/api/term/replay", map[string]string{"id": c.ID}, &rep)
	cancel()
	mu.Lock()
	switch {
	case rerr == nil:
		curRun, sentEnd = rep.Status.Run, rep.End
		if len(rep.Data) > 0 {
			send(websocket.BinaryMessage, rep.Data)
		}
	case code == http.StatusNotFound:
		// not started yet: the first output of its first run is delivered from the start
	default:
		mu.Unlock()
		fail(rerr.Error())
		return
	}
	ready = true
	for _, ch := range pending {
		deliver(ch)
	}
	pending = nil
	mu.Unlock()

	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if kind == websocket.BinaryMessage {
			if len(data) > 64<<10 {
				continue
			}
			_ = h.bus.PublishRaw(natspkg.TermInSubject(c.ID), data)
			continue
		}
		var msg struct {
			Type string `json:"type"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
			b, _ := json.Marshal(msg)
			_ = h.bus.PublishRaw(natspkg.TermCtlSubject(c.ID), b)
		}
	}
}
