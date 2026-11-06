package db

import (
	"database/sql"
	"errors"
	"testing"
)

func TestFirstTurnUsageIsTheFirstTurnWithTokens(t *testing.T) {
	d, err := Init(t.TempDir() + "/first.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := FirstTurnUsage(d, "s1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a session without usage must give ErrNoRows, got %v", err)
	}
	rows := []TurnUsage{
		{ID: "a", SessionID: "s1", Model: "sonnet"}, // an empty turn is skipped
		{ID: "b", SessionID: "s1", Model: "sonnet", InputTokens: 2, CacheCreationInputTokens: 500, CacheReadInputTokens: 30000},
		{ID: "c", SessionID: "s1", Model: "sonnet", InputTokens: 3, CacheReadInputTokens: 31000},
		{ID: "d", SessionID: "s2", Model: "sonnet", CacheCreationInputTokens: 9},
	}
	for _, u := range rows {
		if err := InsertTurnUsage(d, u); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FirstTurnUsage(d, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b" || got.CacheCreationInputTokens != 500 || got.CacheReadInputTokens != 30000 {
		t.Fatalf("want the second row (first with tokens), got %+v", got)
	}
}
