package stt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Model is one way to start the shared voice worker. ID is also the engine key the worker
// reports in GetInfo, so a running worker can be matched against the chosen model.
type Model struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Device      string `json:"device"` // "gpu" | "cpu"
	Memory      string `json:"memory"` // approximate, for the picker
	Recommended bool   `json:"recommended,omitempty"`
}

// Models are the same three choices as SttApp's model list.
var Models = []Model{
	{ID: "nemo-fp16", Label: "Parakeet v3 · GPU fp16", Device: "gpu", Memory: "≈1.7 GB of VRAM", Recommended: true},
	{ID: "nemo", Label: "Parakeet v3 · GPU fp32", Device: "gpu", Memory: "≈5 GB of VRAM"},
	{ID: "sherpa-int8", Label: "Parakeet v3 · CPU int8", Device: "cpu", Memory: "≈0.6 GB of RAM, uses a lot of CPU"},
}

const defaultModel = "nemo-fp16"

// KnownModel reports whether id is one of Models.
func KnownModel(id string) bool {
	_, ok := findModel(id)
	return ok
}

func findModel(id string) (Model, bool) {
	for _, m := range Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

func modelFile() string { return filepath.Join(stateDir(), "stt-model.json") }

// Selected is the model lucode starts the worker with: LUCODE_STT_ENGINE (a model id, or the
// older "sherpa" / "nemo"), else the one chosen in the voice settings, else GPU fp16.
func Selected() string {
	switch v := os.Getenv("LUCODE_STT_ENGINE"); v {
	case "":
	case "sherpa":
		return "sherpa-int8"
	case "nemo":
		// the older name for "the GPU model" (fp16); the fp32 model, whose id is also "nemo", is
		// chosen in the voice settings
		return "nemo-fp16"
	default:
		if _, ok := findModel(v); ok {
			return v
		}
	}
	var saved struct {
		Model string `json:"model"`
	}
	if data, err := os.ReadFile(modelFile()); err == nil && json.Unmarshal(data, &saved) == nil {
		if _, ok := findModel(saved.Model); ok {
			return saved.Model
		}
	}
	return defaultModel
}

func saveSelected(id string) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]string{"model": id})
	return os.WriteFile(modelFile(), data, 0o644)
}

// modelArgs are the worker arguments that pick the model. modelDir is the sherpa model folder.
func modelArgs(id, modelDir string) []string {
	switch id {
	case "sherpa-int8":
		return []string{"--engine-key", id, "--engine", "sherpa", "--model-dir", modelDir}
	case "nemo":
		return []string{"--engine-key", id, "--engine", "nemo"}
	default:
		return []string{"--engine-key", "nemo-fp16", "--engine", "nemo", "--fp16"}
	}
}

// Switch makes id the model of the shared worker. The choice is saved, so a worker lucode
// starts later uses it too. A worker that answers with another model is shut down (it is the
// user's explicit choice; anything connected to it, SttApp included, loses its session) and
// a new one is started, returning once it answers (its model may still be loading). With no
// worker running it only saves the choice and returns a zero Info.
func Switch(ctx context.Context, id string) (Info, error) {
	if _, ok := findModel(id); !ok {
		return Info{}, fmt.Errorf("unknown voice model %q", id)
	}
	launchMu.Lock()
	defer launchMu.Unlock()
	if err := saveSelected(id); err != nil {
		return Info{}, err
	}
	c, info, err := probe(ctx)
	switch {
	case err == nil && info.EngineKey == id:
		c.Close()
		return info, nil
	case err == nil:
		_ = c.Shutdown(ctx)
		c.Close()
		stopped := false
		for i := 0; i < 40 && !stopped; i++ { // up to ~10 s for the old worker to let go of the port
			time.Sleep(250 * time.Millisecond)
			if c2, _, err := probe(ctx); err != nil {
				stopped = true
			} else {
				c2.Close()
			}
		}
		if !stopped {
			return Info{}, errors.New("the running voice worker did not shut down")
		}
	case NeedsToken(err):
		return Info{}, tokenError()
	default:
		// nothing is running: the choice is saved and the worker starts with it the next time
		// somebody needs it (the microphone, or /api/stt/start). No memory is taken now.
		return Info{}, nil
	}
	c, info, err = ensure(ctx)
	if err != nil {
		return Info{}, err
	}
	c.Close()
	return info, nil
}
