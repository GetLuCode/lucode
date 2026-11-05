package db

import (
	"testing"
	"time"
)

func TestUsageAggregatesCarryTheOneHourCacheWrites(t *testing.T) {
	d, err := Init(t.TempDir() + "/1h.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// the per-session aggregate joins sessions, which belong to a project
	if _, err := d.Exec(`INSERT INTO projects (id, name) VALUES ('p1','project')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO sessions (id, project_id, name) VALUES ('s1','p1','one')`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []TurnUsage{
		{ID: "a", SessionID: "s1", ProjectID: "p1", Model: "m", CacheCreationInputTokens: 1000, CacheCreation1hInputTokens: 1000},
		{ID: "b", SessionID: "s1", ProjectID: "p1", Model: "m", CacheCreationInputTokens: 500}, // an older row: 0 of 1h
	} {
		if err := InsertTurnUsage(d, u); err != nil {
			t.Fatal(err)
		}
	}
	models, err := AggregateModelUsage(d, time.Time{}, time.Time{})
	if err != nil || len(models) != 1 || models[0].CacheCreationInputTokens != 1500 || models[0].CacheCreation1h != 1000 {
		t.Fatalf("by model: %+v %v", models, err)
	}
	daily, err := AggregateDailyUsage(d, time.Time{}, time.Time{}, false)
	if err != nil || len(daily) != 1 || daily[0].CacheCreation1h != 1000 {
		t.Fatalf("by day: %+v %v", daily, err)
	}
	sessions, err := AggregateSessionUsage(d, time.Time{}, time.Time{}, "")
	if err != nil || len(sessions) != 1 || sessions[0].CacheCreation1h != 1000 {
		t.Fatalf("by session: %+v %v", sessions, err)
	}
}
