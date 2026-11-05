package db

import (
	"strings"
	"testing"
)

func TestSeedDefaultsCreatesSupervisorOnceAndRespectsDeletion(t *testing.T) {
	d, err := Init(t.TempDir() + "/seed.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for i := 0; i < 2; i++ { // idempotent
		if err := SeedDefaults(d); err != nil {
			t.Fatal(err)
		}
	}
	agents, err := ListAgents(d, "any-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "Supervisor" || agents[0].Scope != "global" {
		t.Fatalf("want one global Supervisor, got %+v", agents)
	}
	if !strings.Contains(strings.Join(agents[0].LucodeTools, ","), "agent_create") {
		t.Fatalf("Supervisor lacks agent_create: %v", agents[0].LucodeTools)
	}

	if err := DeleteAgent(d, agents[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(d); err != nil {
		t.Fatal(err)
	}
	if agents, _ = ListAgents(d, "any-project"); len(agents) != 0 {
		t.Fatalf("a deleted Supervisor must not come back, got %+v", agents)
	}
}
