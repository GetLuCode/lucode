package hub_test

import (
	"context"
	"path/filepath"
	"testing"

	"claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/plugins"
	"claude-orchestrator/internal/plugins/bundled"
	"claude-orchestrator/internal/plugins/hub"
)

// Everything that ships in the binary installs clean through the real pipeline: the zip checks,
// the manifest, and its blueprints against the real flow validator with its own nodes known. A
// blueprint the validator refuses shows up as the plugin's error, so that error must be empty.
func TestEveryBundledPluginInstallsClean(t *testing.T) {
	db, err := dbpkg.Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := dbpkg.InsertProject(db, dbpkg.Project{ID: "p1", Name: "p", WorkDirs: []string{"."}, DefaultModel: "haiku"}); err != nil {
		t.Fatal(err)
	}
	var host plugins.HubHost
	factory := func(d plugins.HubDeps) (plugins.HubHost, error) {
		h, err := hub.New(d)
		host = h
		return h, err
	}
	h := api.New(db, natspkg.NewBus(nil), nil, "")
	if err := h.StartPlugins(context.Background(), t.TempDir(), nil, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flows.SetPluginSource(nil) })

	entries, err := bundled.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	catalog := host.Catalog()
	if len(catalog) != dirs {
		t.Errorf("%d folders in bundled, but %d in the catalog: a manifest does not parse (see the log)", dirs, len(catalog))
	}
	for _, c := range catalog {
		t.Run(c.ID, func(t *testing.T) {
			v, err := host.InstallBundled(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Error != "" || v.Status != "enabled" || v.Source != "bundled" {
				t.Fatalf("status %q source %q error: %s", v.Status, v.Source, v.Error)
			}
			if err := host.Uninstall(c.ID, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
