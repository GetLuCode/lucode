package db

import (
	"testing"
	"time"
)

func TestToolCostsAttributesTheGrowthOfTheContextToTheToolsThatCausedIt(t *testing.T) {
	d, err := Init(t.TempDir() + "/tools.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// a result reported before the call that asked for it must still be matched
	if err := SaveToolResult(d, "s1", "tC", 200, 0, false); err != nil {
		t.Fatal(err)
	}
	calls := []APICall{
		// ctx 1000; asks for two tools in parallel
		{SessionID: "s1", Model: "m", FirstInTurn: true, CacheCreation: 1000, CacheCreation1h: 1000, Output: 50,
			Tools: []ToolUse{{"tA", "Grep"}, {"tB", "Read"}}},
		// ctx 1000+50+(300+100 results) = 1450: 450 written, 1000 read
		{SessionID: "s1", Model: "m", CacheCreation: 450, CacheCreation1h: 450, CacheRead: 1000, Output: 20,
			Tools: []ToolUse{{"tC", "Grep"}}},
		// ctx 1450+20+200 = 1670
		{SessionID: "s1", Model: "m", CacheCreation: 220, CacheCreation1h: 220, CacheRead: 1450, Output: 10},
		// a new turn: the growth since the last call is the user's message, not a tool result
		{SessionID: "s1", Model: "m", FirstInTurn: true, CacheCreation: 900, CacheCreation1h: 900, CacheRead: 1670, Output: 5,
			Tools: []ToolUse{{"tD", "Bash"}}},
	}
	for _, c := range calls {
		if err := InsertAPICall(d, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []struct {
		id    string
		chars int
	}{{"tA", 300}, {"tB", 100}, {"tD", 10}} {
		if err := SaveToolResult(d, "s1", r.id, r.chars, 0, false); err != nil {
			t.Fatal(err)
		}
	}

	first, w1h, err := FirstCallUsage(d, "s1")
	if err != nil || first.CacheCreationInputTokens != 1000 || first.CacheReadInputTokens != 0 || w1h != 1000 {
		t.Fatalf("the first call is the initial context: %+v %d %v", first, w1h, err)
	}
	if _, _, err := FirstCallUsage(d, "nobody"); err == nil {
		t.Fatal("a session without calls must give ErrNoRows")
	}

	if one, err := SessionToolCosts(d, "s1"); err != nil || len(one) == 0 {
		t.Fatalf("the costs of one session: %v %v", one, err)
	}
	if none, err := SessionToolCosts(d, "nobody"); err != nil || len(none) != 0 {
		t.Fatalf("another session has no tools: %v %v", none, err)
	}

	rows, err := ToolCosts(d, time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ToolCostRow{}
	for _, r := range rows {
		got[r.Tool] = r
	}
	grep, read, bash := got["Grep"], got["Read"], got["Bash"]

	// Grep: A (300 tokens) is re-read by calls 3 and 4 (the new turn still carries the context)
	// and C (200 tokens, entered at call 3) by call 4: 300*2 + 200*1
	if grep.Calls != 2 || grep.ResultTokens != 500 || grep.Written1h != 500 || grep.CarriedRead != 800 || grep.OutputTokens != 25+20 {
		t.Fatalf("Grep: %+v", grep)
	}
	if read.Calls != 1 || read.ResultTokens != 100 || read.CarriedRead != 200 || read.OutputTokens != 25 {
		t.Fatalf("Read: %+v", read)
	}
	// Bash asked in the last call of the session: nothing follows, so its result is unmeasured
	if bash.Calls != 1 || bash.Unmeasured != 1 || bash.ResultTokens != 0 {
		t.Fatalf("Bash: %+v", bash)
	}
}

func TestToolCostsKeepsSubAgentsAndProjectsApart(t *testing.T) {
	d, err := Init(t.TempDir() + "/tools2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// the sub-agent's calls sit between the main agent's: they must not count as its "next call"
	for _, c := range []APICall{
		{SessionID: "s", ProjectID: "p1", Model: "m", FirstInTurn: true, CacheCreation: 1000, Output: 10, Tools: []ToolUse{{"t1", "Task"}}},
		{SessionID: "s", ProjectID: "p1", Parent: "t1", Model: "m", CacheCreation: 99999, Output: 10},
		{SessionID: "s", ProjectID: "p1", Model: "m", CacheCreation: 400, CacheRead: 1000, Output: 10},
		{SessionID: "other", ProjectID: "p2", Model: "m", FirstInTurn: true, Output: 1, Tools: []ToolUse{{"t9", "Task"}}},
	} {
		if err := InsertAPICall(d, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveToolResult(d, "s", "t1", 50, 0, false); err != nil {
		t.Fatal(err)
	}
	rows, err := ToolCosts(d, time.Time{}, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Calls != 1 || rows[0].ResultTokens != 390 { // 1400 - 1000 - 10
		t.Fatalf("want one Task row of the main agent with 390 result tokens, got %+v", rows)
	}
}
