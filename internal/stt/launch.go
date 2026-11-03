package stt

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// The worker is the one of SttApp (a Python gRPC server). lucode starts it on demand
// with the small int8 model (about 0.6 GB, runs on CPU) and keeps its port and token
// in ~/.lucode/stt-worker.json so a later lucode finds it again.
//
// Overrides: LUCODE_STT_DIR (folder with stt_worker.py and models\), LUCODE_STT_PYTHON.

const (
	defaultDir    = `C:\projects\fishaudio_oct\SttApp`
	defaultPython = `E:\dev\testing\voice\.venv\Scripts\python.exe`
	modelName     = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"
	voskName      = "vosk-model-small-es-0.42"
	idleSeconds   = 1800 // the worker frees its RAM by itself after 30 minutes without use
)

type state struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
	Vosk  bool   `json:"vosk"` // started with the Vosk model (needed for a custom wake phrase)
}

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode")
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var launchMu sync.Mutex

func readState() (state, bool) {
	var st state
	data, err := os.ReadFile(filepath.Join(stateDir(), "stt-worker.json"))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, true
}

// Running returns a client for the worker if one is alive, without starting anything.
func Running(ctx context.Context) (*Client, Info, bool) {
	data, err := os.ReadFile(filepath.Join(stateDir(), "stt-worker.json"))
	if err != nil {
		return nil, Info{}, false
	}
	var st state
	if json.Unmarshal(data, &st) != nil || st.Port <= 0 {
		return nil, Info{}, false
	}
	c, err := Dial(st.Port, st.Token)
	if err != nil {
		return nil, Info{}, false
	}
	info, err := c.GetInfo(ctx)
	if err != nil {
		c.Close()
		return nil, Info{}, false
	}
	return c, info, true
}

// Ensure returns a client for a running worker, starting one if there is none. It
// returns as soon as the worker answers; the model may still be loading (Info.Ready).
func Ensure(ctx context.Context) (*Client, Info, error) {
	launchMu.Lock()
	defer launchMu.Unlock()

	vosk := filepath.Join(envOr("LUCODE_STT_DIR", defaultDir), "models", voskName)
	_, voskErr := os.Stat(vosk)
	hasVosk := voskErr == nil

	if c, info, ok := Running(ctx); ok {
		st, _ := readState()
		if !hasVosk || st.Vosk {
			return c, info, nil
		}
		// A worker started without the Vosk model cannot detect a custom wake phrase:
		// replace it (the model is reloaded, a few seconds).
		_ = c.Shutdown(ctx)
		c.Close()
		for i := 0; i < 20; i++ {
			time.Sleep(250 * time.Millisecond)
			c2, _, still := Running(ctx)
			if !still {
				break
			}
			c2.Close()
		}
	}

	dir := envOr("LUCODE_STT_DIR", defaultDir)
	python := envOr("LUCODE_STT_PYTHON", defaultPython)
	script := filepath.Join(dir, "stt_worker.py")
	model := filepath.Join(dir, "models", modelName)
	for _, p := range []string{python, script, model} {
		if _, err := os.Stat(p); err != nil {
			return nil, Info{}, fmt.Errorf("voice worker: %s not found (set LUCODE_STT_DIR / LUCODE_STT_PYTHON)", p)
		}
	}

	port, err := freePort()
	if err != nil {
		return nil, Info{}, err
	}
	token := randomToken()
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return nil, Info{}, err
	}
	logf, err := os.Create(filepath.Join(stateDir(), "stt-worker.log"))
	if err != nil {
		return nil, Info{}, err
	}
	cmd := exec.Command(python, "-u", script,
		"--port", fmt.Sprint(port), "--engine-key", "sherpa-int8", "--idle-timeout", fmt.Sprint(idleSeconds),
		"--engine", "sherpa", "--model-dir", model)
	if hasVosk {
		cmd.Args = append(cmd.Args, "--vosk-model-dir", vosk)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "STT_TOKEN="+token, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd) // it must not die with lucode (it frees itself when idle)
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, Info{}, err
	}
	go func() { _ = cmd.Wait(); logf.Close() }()
	data, _ := json.Marshal(state{Port: port, Token: token, PID: cmd.Process.Pid, Vosk: hasVosk})
	_ = os.WriteFile(filepath.Join(stateDir(), "stt-worker.json"), data, 0o600)

	c, err := Dial(port, token)
	if err != nil {
		return nil, Info{}, err
	}
	deadline := time.Now().Add(60 * time.Second) // the port opens before the model loads
	for time.Now().Before(deadline) {
		if info, err := c.GetInfo(ctx); err == nil {
			return c, info, nil
		}
		select {
		case <-ctx.Done():
			c.Close()
			return nil, Info{}, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	c.Close()
	return nil, Info{}, errors.New("voice worker did not start (see ~/.lucode/stt-worker.log)")
}

// WaitReady blocks until the model is loaded.
func WaitReady(ctx context.Context, c *Client, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if info, err := c.GetInfo(ctx); err == nil && info.Ready {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the voice model did not finish loading")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
