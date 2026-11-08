package hub

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"claude-orchestrator/internal/cdp"
	"claude-orchestrator/internal/plugins"
)

// geminiSource is the source of the conversations that plugin keeps.
const geminiSource = "aimode"

// geminiPort is where that Edge has always listened, so one still open is found again.
const geminiPort = 9333

// edgeDriver is a plugin's own Edge: its own profile and a port the host picks, never the browser
// the browser_* tools share.
type edgeDriver struct {
	b       *cdp.Browser
	profile string
	portAt  string

	mu   sync.Mutex
	port int
}

// edgeSetup says which Edge a plugin gets. The profile that holds the Google login goes only to the
// bundled Gemini plugin: the id alone is not enough, and an uploaded plugin never gets it.
func edgeSetup(dataDir, pluginID, source string) (name string, port int, profile string) {
	name = "plugin-" + pluginID
	profile = filepath.Join(dataDir, "plugin-data", pluginID, "browser-profile")
	if pluginID != plugins.GeminiID || source != sourceBundled {
		return name, 0, profile
	}
	if p := os.Getenv("LUCODE_AIMODE_PROFILE"); p != "" {
		return "aimode", geminiPort, p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return "aimode", geminiPort, filepath.Join(home, ".lucode", "aimode-profile")
	}
	return name, 0, profile
}

func newEdgeDriver(dataDir, pluginID, source string) *edgeDriver {
	name, port, profile := edgeSetup(dataDir, pluginID, source)
	return &edgeDriver{
		b: cdp.New(name, port), profile: profile, port: port,
		portAt: filepath.Join(dataDir, "plugin-data", pluginID, "browser-port"),
	}
}

// choosePort keeps the port a plugin's Edge had, so one that outlived a restart is found again.
func (d *edgeDriver) choosePort() (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.port != 0 {
		return d.port, nil
	}
	if raw, err := os.ReadFile(d.portAt); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && p > 0 {
			d.port = p
			return p, nil
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	d.port = l.Addr().(*net.TCPAddr).Port
	l.Close()
	if err := os.MkdirAll(filepath.Dir(d.portAt), 0o755); err != nil {
		return 0, err
	}
	_ = os.WriteFile(d.portAt, []byte(strconv.Itoa(d.port)), 0o644)
	return d.port, nil
}

func (d *edgeDriver) Launch(headless bool) (string, error) {
	port, err := d.choosePort()
	if err != nil {
		return "", err
	}
	return d.b.Launch(cdp.LaunchOptions{Headless: headless, Port: port, ProfileDir: d.profile})
}
func (d *edgeDriver) Close() string  { return d.b.Close() }
func (d *edgeDriver) Status() string { return d.b.Status() }
func (d *edgeDriver) Tabs() (any, error) {
	return d.b.Tabs()
}
func (d *edgeDriver) NewTab(url string) (any, error)   { return d.b.NewTab(url) }
func (d *edgeDriver) SwitchTab(id string) (any, error) { return d.b.SwitchTab(id) }
func (d *edgeDriver) CloseTab(id string) error         { return d.b.CloseTab(id) }
func (d *edgeDriver) URL() (string, error) {
	i, err := d.b.Info()
	return i.URL, err
}
func (d *edgeDriver) Info() (any, error)               { return d.b.Info() }
func (d *edgeDriver) Navigate(url string) (any, error) { return d.b.Navigate(url) }
func (d *edgeDriver) Find(role, name, selector string, exact bool, limit int) (any, error) {
	return d.b.Find(cdp.FindOptions{Role: role, Name: name, Selector: selector, Exact: exact, Limit: limit})
}
func (d *edgeDriver) Click(ref string) (any, error) {
	r, err := d.b.Click(cdp.ClickOptions{Ref: ref})
	return fmt.Sprint(r), err
}
func (d *edgeDriver) Type(ref, text string, submit, clear bool) (any, error) {
	r, err := d.b.Type(cdp.TypeOptions{Ref: ref, Text: text, Submit: submit, Clear: clear})
	return fmt.Sprint(r), err
}
func (d *edgeDriver) Press(combo string) (any, error) {
	r, err := d.b.Press(combo)
	return fmt.Sprint(r), err
}
func (d *edgeDriver) Eval(js string) (string, error)           { return d.b.Eval(js) }
func (d *edgeDriver) Text(ref string, max int) (string, error) { return d.b.Text(ref, max) }

var _ plugins.BrowserDriver = (*edgeDriver)(nil)
