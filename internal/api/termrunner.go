package api

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/termrun"
)

// The Commands tab, runner side. The processes live on the machine of a RUNNER, in a pseudo-terminal
// (internal/termrun). Orders (start, stop, replay) arrive as RPC at /api/term/*; the bytes of the
// terminal travel on NATS subjects (natsbus/term.go), not through the RPC, so output is live. The
// hub decides what may run: the runner only runs what the hub's stored definitions send it.

type termNATS struct{ bus *natspkg.Bus }

func (t termNATS) Output(id string, run, off uint64, data []byte) {
	msg := make([]byte, 16+len(data))
	binary.BigEndian.PutUint64(msg, run)
	binary.BigEndian.PutUint64(msg[8:], off)
	copy(msg[16:], data)
	_ = t.bus.PublishRaw(natspkg.TermOutSubject(id), msg)
}

func (t termNATS) Status(s termrun.Status) {
	data, _ := json.Marshal(s)
	_ = t.bus.PublishRaw(natspkg.TermStatusSubject, data)
}

// termState is what a Handler keeps for the Commands tab.
type termState struct {
	once  sync.Once
	mgr   *termrun.Manager // the runner's processes
	watch sync.Once        // the hub's subscription to their state
}

// terminals is the runner's process manager, with its subjects subscribed on first use.
func (h *Handler) terminals() *termrun.Manager {
	h.term.once.Do(func() {
		mgr := termrun.NewManager(termNATS{h.bus})
		h.term.mgr = mgr
		_, _ = h.bus.SubscribeRaw("lucode.term.*.in", func(subject string, data []byte) {
			mgr.Write(termIDFromSubject(subject), data)
		})
		_, _ = h.bus.SubscribeRaw("lucode.term.*.ctl", func(subject string, data []byte) {
			var c struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &c) == nil && c.Type == "resize" {
				mgr.Resize(termIDFromSubject(subject), c.Cols, c.Rows)
			}
		})
	})
	return h.term.mgr
}

// StopTerminals ends the commands the Commands tab started on this runner. The runner calls it when
// it stops, so nothing it launched is left running without anyone to show it.
func (h *Handler) StopTerminals() {
	if h.term.mgr != nil {
		h.term.mgr.Shutdown()
	}
}

// termIDFromSubject takes the id out of lucode.term.<id>.<kind>.
func termIDFromSubject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

// termCwd is where a command runs: the project's work dir on this runner plus the relative path.
// The result stays inside the work dir.
func (h *Handler) termCwd(base, rel string) (string, error) {
	if base == "" {
		return "", errors.New("the project has no work directory on this runner")
	}
	if h.manager != nil {
		base = h.manager.ResolveWorkDirs([]string{base})[0]
	}
	base = filepath.Clean(base)
	if rel == "" {
		return base, nil
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(filepath.ToSlash(rel), "/") || filepath.VolumeName(rel) != "" {
		return "", errors.New("the folder must be relative to the project's work directory")
	}
	dir := filepath.Join(base, rel)
	if back, err := filepath.Rel(base, dir); err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", errors.New("the folder must stay inside the project's work directory")
	}
	return dir, nil
}

// termShells is every shell this runner offers: the ones it found, then the ones registered.
func (h *Handler) termShells() ([]termrun.Shell, error) {
	list := termrun.Detect()
	custom, err := dbpkg.ListTermShells(h.db)
	if err != nil {
		return nil, err
	}
	for _, c := range custom {
		list = append(list, termrun.Shell{ID: c.ID, Name: c.Name, Path: c.Path, Args: c.Args})
	}
	return list, nil
}

// resolveShell finds a shell by id among this runner's; "" is the system default.
func (h *Handler) resolveShell(id string) (termrun.Shell, error) {
	if id == "" {
		return termrun.Shell{}, nil
	}
	if s, ok := termrun.Find(id); ok {
		return s, nil
	}
	if c, err := dbpkg.GetTermShell(h.db, id); err == nil {
		return termrun.Shell{ID: c.ID, Name: c.Name, Path: c.Path, Args: c.Args}, nil
	}
	return termrun.Shell{}, fmt.Errorf("this runner has no shell %q: pick another one, or register it on this runner", id)
}

type termStartReq struct {
	ID      string `json:"id"`
	Base    string `json:"base"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"`
	// Env is added to the runner's own environment. A runner older than this field ignores it.
	Env []dbpkg.EnvVar `json:"env,omitempty"`
	// Shell is the id of one of this runner's shells (GET /api/term/shells); "" = the system default.
	Shell string `json:"shell,omitempty"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
}

func (h *Handler) registerTermRPC(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/term/start", func(w http.ResponseWriter, r *http.Request) {
		var req termStartReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" || strings.ContainsAny(req.ID, ".*> ") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		dir, err := h.termCwd(req.Base, req.Cwd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// the runner checks again what the hub already checked: it is the one that runs it
		env, err := cleanEnv(req.Env)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pairs := make([]string, len(env))
		for i, v := range env {
			pairs[i] = v.Name + "=" + v.Value
		}
		sh, err := h.resolveShell(req.Shell)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		st, err := h.terminals().Start(termrun.Spec{ID: req.ID, Command: req.Command, Cwd: dir, Env: pairs, Shell: sh, Cols: req.Cols, Rows: req.Rows})
		if err != nil {
			if err.Error() == "already running" {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		jsonResp(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /api/term/shells", func(w http.ResponseWriter, r *http.Request) {
		list, err := h.termShells()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{"shells": list})
	})
	// registering a shell is a thing the runner does and keeps: the hub only forwards the order
	mux.HandleFunc("POST /api/term/shells", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string   `json:"name"`
			Path string   `json:"path"`
			Args []string `json:"args"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name, path, args, err := termrun.CheckCustom(in.Name, in.Path, in.Args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if list, err := h.termShells(); err == nil {
			for _, s := range list {
				if strings.EqualFold(s.Name, name) {
					http.Error(w, "this runner already has a shell called "+name, http.StatusConflict)
					return
				}
			}
		}
		saved, err := dbpkg.InsertTermShell(h.db, dbpkg.TermShell{Name: name, Path: path, Args: args})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResp(w, http.StatusOK, termrun.Shell{ID: saved.ID, Name: saved.Name, Path: saved.Path, Args: saved.Args})
	})
	mux.HandleFunc("DELETE /api/term/shells/{id}", func(w http.ResponseWriter, r *http.Request) {
		ok, err := dbpkg.DeleteTermShell(h.db, r.PathValue("id")) // only registered ones are in the table
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "that shell is not one registered on this runner", http.StatusNotFound)
			return
		}
		jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/term/stop", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := h.terminals().Stop(req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/term/forget", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		h.terminals().Forget(req.ID)
		jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
	})
	// replay: the output kept for a process. The hub subscribes to the live subject first and cuts
	// the overlap with the offsets, so nothing is lost or repeated between the two.
	mux.HandleFunc("POST /api/term/replay", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data, start, end, st, ok := h.terminals().Replay(req.ID)
		if !ok {
			http.Error(w, "not running", http.StatusNotFound)
			return
		}
		jsonResp(w, http.StatusOK, termReplay{Data: data, Start: start, End: end, Status: st})
	})
	mux.HandleFunc("GET /api/term/list", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, http.StatusOK, h.terminals().List())
	})
}

type termReplay struct {
	Data   []byte         `json:"data"` // base64 in JSON
	Start  uint64         `json:"start"`
	End    uint64         `json:"end"`
	Status termrun.Status `json:"status"`
}
