package stt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The worker is the one of SttApp (a Python gRPC server). There is ONE shared worker per
// machine, on a fixed port (50700) and without a token: it listens on 127.0.0.1 only and every
// app that needs speech (lucode, SttApp) connects to whichever worker answers there. lucode
// starts one only when nothing answers, detached so it outlives lucode (it frees itself after
// 30 minutes without use), with Parakeet TDT 0.6B v3 (NeMo, fp16) on the GPU.
//
// A worker that is already there is used as it is, whatever started it and with whatever
// model: lucode never shuts it down or replaces it on its own. Only the user's explicit choice
// of another model in the voice settings does (Switch, model.go).
//
// The model it starts is Selected(): LUCODE_STT_ENGINE (a model id, or "sherpa" for the int8
// CPU model), else the one chosen in the voice settings, else GPU fp16.
//
// Overrides: LUCODE_STT_PORT (port, default 50700), LUCODE_STT_DIR (folder with stt_worker.py
// and models\), LUCODE_STT_PYTHON.

const (
	defaultPort   = 50700 // same as DEFAULT_GRPC_PORT in stt_worker.py
	defaultDir    = `C:\projects\fishaudio_oct\SttApp`
	defaultPython = `E:\dev\testing\voice\.venv\Scripts\python.exe`
	modelName     = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"
	voskName      = "vosk-model-small-es-0.42"
	idleSeconds   = 1800 // the worker frees its RAM by itself after 30 minutes without use
)

// Port is the gRPC port of the shared worker.
func Port() int {
	if n, err := strconv.Atoi(os.Getenv("LUCODE_STT_PORT")); err == nil && n > 0 && n < 65536 {
		return n
	}
	return defaultPort
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

var launchMu sync.Mutex

// probe asks whatever listens on the shared port. A nil error means a worker answered.
func probe(ctx context.Context) (*Client, Info, error) {
	c, err := Dial(Port())
	if err != nil {
		return nil, Info{}, err
	}
	info, err := c.GetInfo(ctx)
	if err != nil {
		c.Close()
		return nil, Info{}, err
	}
	return c, info, nil
}

// Running returns a client for the worker if one answers on the shared port, without
// starting anything. It is any worker: the one lucode started or one started by SttApp.
func Running(ctx context.Context) (*Client, Info, bool) {
	c, info, err := probe(ctx)
	if err != nil {
		return nil, Info{}, false
	}
	return c, info, true
}

// Ensure returns a client for the shared worker, starting one if none answers. It returns as
// soon as the worker answers; the model may still be loading (Info.Ready).
func Ensure(ctx context.Context) (*Client, Info, error) {
	launchMu.Lock()
	defer launchMu.Unlock()
	return ensure(ctx)
}

// tokenError is what a worker that asks for a token gets: it was started with one (an older
// SttApp). Starting another on the same port would fail, and it is not ours to stop.
func tokenError() error {
	return fmt.Errorf("the voice worker on port %d asks for a token; restart it with --no-token (or set LUCODE_STT_PORT to another port)", Port())
}

// ensure is Ensure without the lock (the caller holds launchMu).
func ensure(ctx context.Context) (*Client, Info, error) {
	port := Port()
	c, info, err := probe(ctx)
	if err == nil {
		return c, info, nil
	}
	if NeedsToken(err) {
		return nil, Info{}, tokenError()
	}

	dir := envOr("LUCODE_STT_DIR", defaultDir)
	python := envOr("LUCODE_STT_PYTHON", defaultPython)
	script := filepath.Join(dir, "stt_worker.py")
	model := filepath.Join(dir, "models", modelName)
	vosk := filepath.Join(dir, "models", voskName)
	_, voskErr := os.Stat(vosk)
	hasVosk := voskErr == nil

	chosen := Selected()
	need := []string{python, script}
	if chosen == "sherpa-int8" {
		need = append(need, model) // the GPU model comes from the Hugging Face cache instead
	}
	for _, p := range need {
		if _, err := os.Stat(p); err != nil {
			return nil, Info{}, fmt.Errorf("voice worker: %s not found (set LUCODE_STT_DIR / LUCODE_STT_PYTHON)", p)
		}
	}
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return nil, Info{}, err
	}
	logf, err := os.Create(filepath.Join(stateDir(), "stt-worker.log"))
	if err != nil {
		return nil, Info{}, err
	}

	args := []string{"-u", script, "--port", fmt.Sprint(port), "--no-token", "--idle-timeout", fmt.Sprint(idleSeconds)}
	args = append(args, modelArgs(chosen, model)...)
	if hasVosk {
		args = append(args, "--vosk-model-dir", vosk)
	}
	cmd := exec.Command(python, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd) // it must not die with lucode (it frees itself when idle)
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, Info{}, err
	}
	go func() { _ = cmd.Wait(); logf.Close() }()

	// The port opens before the model loads. If another app started a worker at the same moment,
	// ours exits (port taken) and the probe below finds the other one.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if c, info, err := probe(ctx); err == nil {
			return c, info, nil
		}
		select {
		case <-ctx.Done():
			return nil, Info{}, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
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
