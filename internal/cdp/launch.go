package cdp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultPort is the debugging port lucode uses unless told otherwise.
const DefaultPort = 9222

// LaunchOptions say how to start (or reuse) the browser.
type LaunchOptions struct {
	Headless   bool   // no window; otherwise a normal visible window
	Port       int    // remote debugging port (default 9222)
	URL        string // first page (default about:blank)
	ProfileDir string // --user-data-dir (default ~/.lucode/edge-profile): not the user's daily profile
	Width      int    // window size, default 1280x900
	Height     int
}

// Browser is an Edge instance lucode controls. Default is the one the browser_* tools
// share; New makes others (e.g. for a local "model") that never interfere with it.
type Browser struct {
	name        string // names its state file; "" for Default
	defaultPort int    // port to look at when nothing else is known; 0 = DefaultPort

	mu       sync.Mutex
	port     int
	cmd      *exec.Cmd // nil when attached to a browser somebody else started
	headless bool
	pid      int // Edge lucode started in an earlier run (found again through the state file)
	c        *conn
	sessions map[string]string // target id -> session id
	current  string            // target id of the tab actions go to
	dialogs  map[string]string // session id -> message of the open JavaScript dialog
	mouseX   float64
	mouseY   float64
}

// Default is the browser shared by every tool.
var Default = &Browser{}

// New makes an independent browser: its own state file (so it is found again after a
// restart), its own default port, and no relation to Default. Give it its own profile
// directory in LaunchOptions as well.
func New(name string, port int) *Browser { return &Browser{name: name, defaultPort: port} }

// FindEdge returns the path of msedge: LUCODE_EDGE_BIN, then the usual install locations, then PATH.
func FindEdge() (string, error) {
	if p := os.Getenv("LUCODE_EDGE_BIN"); p != "" {
		return p, nil
	}
	var cands []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			if base := os.Getenv(env); base != "" {
				cands = append(cands, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
	case "darwin":
		cands = append(cands, "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge")
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, name := range []string{"msedge", "microsoft-edge", "microsoft-edge-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Microsoft Edge not found: install it or set LUCODE_EDGE_BIN to msedge")
}

func versionURL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d/json/version", port) }

// wsEndpoint asks the debugging port for the browser websocket; error when nothing listens.
func wsEndpoint(port int) (string, error) {
	cl := http.Client{Timeout: 2 * time.Second}
	resp, err := cl.Get(versionURL(port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.WS == "" {
		return "", errors.New("the debugging port did not return a websocket url")
	}
	return v.WS, nil
}

// Launch starts Edge with a debugging port, or reuses the one already there. A browser lucode
// started in the other mode is restarted; one started by someone else is only attached to.
func (b *Browser) Launch(o LaunchOptions) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o.Port == 0 {
		o.Port = b.knownPort()
	}
	if b.c != nil && b.c.alive() && b.owned() && b.headless != o.Headless {
		b.closeLocked() // the user asked for the other mode
	}
	if b.c != nil && !b.c.alive() {
		b.resetLocked()
	}
	mode := map[bool]string{true: "headless", false: "window"}
	if b.c != nil {
		return fmt.Sprintf("Edge is already running (%s, port %d).", b.modeLocked(), b.port), nil
	}

	if ws, err := wsEndpoint(o.Port); err == nil { // somebody (maybe a previous run) already listens there
		if err := b.connectLocked(ws, o.Port); err != nil {
			return "", err
		}
		if b.owned() && b.headless != o.Headless {
			b.closeLocked() // lucode's own Edge from an earlier run, in the other mode: start it again below
		} else {
			if o.URL != "" {
				if _, err := b.navigateLocked(o.URL); err != nil {
					return "", err
				}
			}
			if b.owned() {
				return fmt.Sprintf("Reconnected to the Edge lucode started earlier (%s, port %d).", b.modeLocked(), o.Port), nil
			}
			return fmt.Sprintf("Attached to the Edge already listening on port %d (it keeps its own mode; browser_close only detaches).", o.Port), nil
		}
	}

	bin, err := FindEdge()
	if err != nil {
		return "", err
	}
	profile := o.ProfileDir
	if profile == "" {
		home, _ := os.UserHomeDir()
		profile = filepath.Join(home, ".lucode", "edge-profile")
	}
	if err := os.MkdirAll(profile, 0o755); err != nil {
		return "", err
	}
	if o.Width == 0 {
		o.Width = 1280
	}
	if o.Height == 0 {
		o.Height = 900
	}
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", o.Port),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		fmt.Sprintf("--window-size=%d,%d", o.Width, o.Height),
		"--no-first-run", "--no-default-browser-check", "--disable-sync",
	}
	if o.Headless {
		args = append(args, "--headless=new")
	}
	if o.URL == "" {
		o.URL = "about:blank"
	}
	args = append(args, o.URL)
	cmd := exec.Command(bin, args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start Edge: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	var ws string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ws, err = wsEndpoint(o.Port); err == nil {
			break
		}
		select {
		case <-exited:
			// Edge hands the URL to an instance already using this profile and exits.
			return "", fmt.Errorf("Edge exited right away: another Edge is probably using the profile %s without a debugging port; close it or pass another profile_dir", profile)
		case <-time.After(150 * time.Millisecond):
		}
	}
	if ws == "" {
		killTree(cmd.Process.Pid)
		return "", fmt.Errorf("Edge did not open the debugging port %d in time", o.Port)
	}
	b.cmd, b.headless = cmd, o.Headless
	if err := b.connectLocked(ws, o.Port); err != nil {
		killTree(cmd.Process.Pid)
		b.cmd = nil
		return "", err
	}
	saveState(b.name, state{Port: o.Port, PID: cmd.Process.Pid, Headless: o.Headless, Profile: profile})
	return fmt.Sprintf("Edge started (%s, port %d, profile %s).", mode[o.Headless], o.Port, profile), nil
}

// owned reports whether lucode started the connected Edge (in this run or an earlier one).
func (b *Browser) owned() bool { return b.cmd != nil || b.pid > 0 }

// knownPort is the debugging port to look at when none is given: the connected one, the one
// lucode used last time, or the default.
func (b *Browser) knownPort() int {
	if b.port != 0 {
		return b.port
	}
	if st, ok := loadState(b.name); ok {
		return st.Port
	}
	if b.defaultPort != 0 {
		return b.defaultPort
	}
	return DefaultPort
}

func (b *Browser) modeLocked() string {
	switch {
	case !b.owned():
		return "started by someone else"
	case b.headless:
		return "headless"
	}
	return "window"
}

func (b *Browser) connectLocked(ws string, port int) error {
	c, err := dial(ws, b.onEvent)
	if err != nil {
		return fmt.Errorf("connect to the browser: %w", err)
	}
	b.c, b.port = c, port
	if b.cmd == nil { // not started in this run: was it lucode's Edge from an earlier one?
		b.pid = 0
		if st, ok := loadState(b.name); ok && st.Port == port {
			b.pid, b.headless = st.PID, st.Headless
		}
	}
	b.sessions = map[string]string{}
	dialogMu.Lock()
	b.dialogs = map[string]string{}
	dialogMu.Unlock()
	b.current = ""
	return nil
}

func (b *Browser) onEvent(session, method string, params json.RawMessage) {
	switch method {
	case "Page.javascriptDialogOpening":
		var p struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		_ = json.Unmarshal(params, &p)
		b.dialogsSet(session, fmt.Sprintf("%s: %s", p.Type, p.Message))
	case "Page.javascriptDialogClosed":
		b.dialogsSet(session, "")
	}
}

// dialogsSet is called from the reader goroutine, which must never wait on b.mu
// (a command that holds it may be waiting for that very goroutine), so it has its own lock.
var dialogMu sync.Mutex

func (b *Browser) dialogsSet(session, msg string) {
	dialogMu.Lock()
	defer dialogMu.Unlock()
	if b.dialogs == nil {
		return
	}
	if msg == "" {
		delete(b.dialogs, session)
	} else {
		b.dialogs[session] = msg
	}
}

func (b *Browser) dialogOf(session string) string {
	dialogMu.Lock()
	defer dialogMu.Unlock()
	return b.dialogs[session]
}

func (b *Browser) resetLocked() {
	if b.c != nil {
		b.c.close()
	}
	b.c, b.sessions, b.current = nil, nil, ""
	dialogMu.Lock()
	b.dialogs = nil
	dialogMu.Unlock()
}

// Close closes the browser lucode started, or just detaches from one it only attached to.
func (b *Browser) Close() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c == nil && !b.owned() {
		// after a restart of lucode: reconnect first so an Edge it started earlier can still be closed
		if err := b.ensureLocked(); err != nil {
			return "No browser was open."
		}
	}
	owned := b.owned()
	b.closeLocked()
	if owned {
		return "Edge closed."
	}
	return "Detached from the browser (it was not started by lucode, so it stays open)."
}

func (b *Browser) closeLocked() {
	if b.c != nil && b.c.alive() && b.owned() {
		_ = b.c.call("", "Browser.close", nil, nil, 3*time.Second)
	}
	cmd, pid, port := b.cmd, b.pid, b.port
	wasOwned := b.owned()
	b.resetLocked()
	b.cmd, b.pid = nil, 0
	if wasOwned {
		clearState(b.name)
	}
	if cmd == nil {
		if pid > 0 { // Edge from an earlier run: wait for the port to go quiet, then make sure
			for i := 0; i < 20; i++ {
				if _, err := wsEndpoint(port); err != nil {
					break
				}
				time.Sleep(150 * time.Millisecond)
			}
			if _, err := wsEndpoint(port); err == nil {
				killTree(pid)
			}
		}
		return
	}
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	killTree(cmd.Process.Pid) // helpers that outlived the main process
}

// ensureLocked makes sure there is a connection, attaching to the default port when
// the browser was started outside (or before a restart of) lucode.
func (b *Browser) ensureLocked() error {
	if b.c != nil && b.c.alive() {
		return nil
	}
	if b.c != nil {
		b.resetLocked()
	}
	port := b.knownPort()
	ws, err := wsEndpoint(port)
	if err != nil {
		return fmt.Errorf("no browser is open on port %d: call browser_launch first", port)
	}
	return b.connectLocked(ws, port)
}

// Status says whether a browser is connected.
func (b *Browser) Status() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c == nil || !b.c.alive() {
		return "No browser is connected."
	}
	return fmt.Sprintf("Edge connected (%s, port %d).", strings.ToLower(b.modeLocked()), b.port)
}
