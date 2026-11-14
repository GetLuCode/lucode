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
	in.ProjectID = p.ID
	if cid := r.PathValue("cid"); cid != "" {
		old, err := dbpkg.GetProjectCommand(h.db, cid)
		if err != nil || old.ProjectID != p.ID {
			http.Error(w, "command not found", http.StatusNotFound)
			return
		}
		in.ID = cid
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
		ID: c.ID, Base: base, Cwd: c.Cwd, Command: c.Command, Cols: cols, Rows: rows,
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
