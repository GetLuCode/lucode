// Package runnercfg is the runner's own settings file: profiles that say which hub to connect to, with
// the token the hub handed out at login, and where on this machine the agents work.
// `lucode-runner auth login`, `configure` and `start` read and write it.
package runnercfg

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultProfile is the profile used when none is named.
const DefaultProfile = "default"

// Profile is one hub plus this machine's settings for it.
type Profile struct {
	Hub        string `json:"hub"`                // https://lucode.home
	NATS       string `json:"nats"`               // wss://lucode.home, no credentials in it
	Token      string `json:"nats_token"`         // what the hub gave at login
	User       string `json:"user,omitempty"`     // who logged in
	WorkingDir string `json:"working_dir"`        // root folder the hub may browse
	CAFile     string `json:"ca_file,omitempty"`  // CA that signed the hub's certificate, if the system does not know it
	DataDir    string `json:"data_dir,omitempty"` // default: the runner's own folder
	LoggedInAt string `json:"logged_in_at,omitempty"`
}

// Config is the whole file.
type Config struct {
	Current  string              `json:"current"`
	Profiles map[string]*Profile `json:"profiles"`
}

// DefaultDir is the runner's folder: its database, identity and this file.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode-runner"
	}
	return filepath.Join(home, ".lucode-runner")
}

// DefaultPath is where the settings file lives.
func DefaultPath() string { return filepath.Join(DefaultDir(), "config.json") }

// Load reads the file; a missing file is an empty configuration.
func Load(path string) (*Config, error) {
	c := &Config{Profiles: map[string]*Profile{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", path, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	return c, nil
}

// Save writes the file with mode 0600 (it holds the hub's token), replacing it in one step.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Name resolves a profile name: the one given, else the current one, else "default".
func (c *Config) Name(name string) string {
	switch {
	case name != "":
		return name
	case c.Current != "":
		return c.Current
	}
	return DefaultProfile
}

// Get returns the named profile, or nil.
func (c *Config) Get(name string) *Profile { return c.Profiles[c.Name(name)] }

// Ensure returns the named profile, creating it and making it the current one.
func (c *Config) Ensure(name string) (string, *Profile) {
	name = c.Name(name)
	p := c.Profiles[name]
	if p == nil {
		p = &Profile{}
		c.Profiles[name] = p
	}
	c.Current = name
	return name, p
}

// NATSURL is the address the runner connects to: the hub's NATS URL with the token as its user info.
func (p *Profile) NATSURL() (string, error) {
	if p.NATS == "" || p.Token == "" {
		return "", errors.New("not logged in: run `lucode-runner auth login`")
	}
	u, err := url.Parse(p.NATS)
	if err != nil {
		return "", err
	}
	u.User = url.User(p.Token)
	return u.String(), nil
}

// Data is the runner's data folder for this profile.
func (p *Profile) Data() string {
	if p.DataDir != "" {
		return p.DataDir
	}
	return DefaultDir()
}

// NormalizeHub completes what is missing from a hub address: "lucode.home" is https://lucode.home, and a
// host with a port ("a.home:8190") is plain http, as a hub on the local network answers there.
func NormalizeHub(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("the hub address is empty")
	}
	if !strings.Contains(s, "://") {
		if strings.Contains(s, ":") {
			s = "http://" + s
		} else {
			s = "https://" + s
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a hub address", s)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("the hub address must be http or https, not %q", u.Scheme)
	}
	return u.Scheme + "://" + u.Host, nil
}

// NATSBase is the WebSocket address of the hub's NATS for a hub address: https gives wss, http gives ws.
func NATSBase(hub string) string {
	if strings.HasPrefix(hub, "https://") {
		return "wss://" + strings.TrimPrefix(hub, "https://")
	}
	return "ws://" + strings.TrimPrefix(hub, "http://")
}

// HTTPClient trusts the system's CAs plus caFile, if there is one.
func HTTPClient(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s has no certificate in it", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}, nil
}

// Login signs in to the hub as a user and asks it for the NATS token. The password is used once and
// never kept: only the token the hub hands out is saved.
func Login(ctx context.Context, hub, user, password, caFile string) (token string, err error) {
	client, err := HTTPClient(caFile)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"username": user, "password": password})
	resp, err := post(ctx, client, hub+"/api/auth/login", "", body)
	if err != nil {
		return "", err
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp, &login); err != nil || login.Token == "" {
		return "", errors.New("the hub did not return a session")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hub+"/api/runner/enroll", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	r, err := client.Do(req)
	if err != nil {
		return "", explain(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	switch r.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusNotImplemented:
		return "", errors.New("this hub does not run its own NATS (no --embedded-nats): connect with `lucode-runner --nats <url>` instead")
	default:
		return "", fmt.Errorf("the hub refused: %s %s", r.Status, strings.TrimSpace(string(b)))
	}
	var enroll struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &enroll); err != nil || enroll.Token == "" {
		return "", errors.New("the hub did not return a NATS token")
	}
	return enroll.Token, nil
}

func post(ctx context.Context, c *http.Client, u, bearer string, body []byte) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	r, err := c.Do(req)
	if err != nil {
		return nil, explain(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	switch r.StatusCode {
	case http.StatusOK:
		return b, nil
	case http.StatusUnauthorized:
		return nil, errors.New("wrong user or password")
	}
	return nil, fmt.Errorf("the hub answered %s %s", r.Status, strings.TrimSpace(string(b)))
}

// explain turns a certificate error into what to do about it.
func explain(err error) error {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	if errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &host) {
		return fmt.Errorf("%w\nthis machine does not trust the hub's certificate: pass the CA that signed it with --ca-file <ca.pem>", err)
	}
	return err
}
