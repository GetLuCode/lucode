package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// rawStream is a stored stream event in the shape the hub persists: the claude line, with the
// API event inside and the id of the call it belongs to.
func rawStream(msgID, apiEvent string) string {
	return fmt.Sprintf(`{"type":"stream_event","session_id":"s","event":{"type":"stream_event","session_id":"s",`+
		`"parent_tool_use_id":null,"api_message_id":%q,"event":%s}}`, msgID, apiEvent)
}

func startEvent(msgID string, cc, cr int) string {
	return rawStream(msgID, fmt.Sprintf(`{"type":"message_start","message":{"id":%q,"model":"claude-sonnet-5-5","usage":{`+
		`"input_tokens":0,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,`+
		`"cache_creation":{"ephemeral_1h_input_tokens":%d,"ephemeral_5m_input_tokens":0}}}}`, msgID, cc, cr, cc))
}

func toolEvent(msgID, id, name string) string {
	return rawStream(msgID, fmt.Sprintf(`{"type":"content_block_start","content_block":{"type":"tool_use","id":%q,"name":%q}}`, id, name))
}

func deltaEvent(msgID string, out int) string {
	return rawStream(msgID, fmt.Sprintf(`{"type":"message_delta","usage":{"output_tokens":%d}}`, out))
}

func TestBackfillRebuildsCallsFromRawEvents(t *testing.T) {
	d, err := Init(t.TempDir() + "/backfill.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`INSERT INTO projects (id, name) VALUES ('p1','project')`,
		`INSERT INTO sessions (id, project_id, name) VALUES ('s1','p1','old'), ('s2','p1','live')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	type call struct {
		id     string
		cc, cr int
		out    int
		tools  [][2]string
		secs   int
	}
	// turn A: a call asks for Read and Grep in parallel, the next answers; turn B: Bash, then an answer
	calls := []call{
		{"msg_1", 1000, 0, 80, [][2]string{{"tr1", "Read"}, {"tg1", "Grep"}}, 1},
		{"msg_2", 450, 1000, 20, nil, 2},
		{"msg_3", 900, 1450, 5, [][2]string{{"tb1", "Bash"}}, 3},
		{"msg_4", 65, 2350, 7, nil, 4},
	}
	insert := func(session, raw string, at time.Time) {
		if _, err := d.Exec(`INSERT INTO raw_events (id, session_id, turn_index, event, created_at) VALUES (?,?,0,?,?)`,
			uuid.New().String(), session, raw, at.Format("2006-01-02T15:04:05.000Z")); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range calls {
		at := base.Add(time.Duration(c.secs) * time.Second)
		// stored out of order on purpose, all with the same time: they were inserted asynchronously
		insert("s1", deltaEvent(c.id, c.out), at)
		for _, tl := range c.tools {
			insert("s1", toolEvent(c.id, tl[0], tl[1]), at)
		}
		insert("s1", startEvent(c.id, c.cc, c.cr), at)
	}
	// a session that already reports live: left alone
	if err := InsertAPICall(d, APICall{SessionID: "s2", ProjectID: "p1", Model: "m", CacheCreation: 5}); err != nil {
		t.Fatal(err)
	}
	insert("s2", startEvent("msg_x", 111, 0), base)

	n, err := BackfillToolUsage(d)
	if err != nil || n != 4 {
		t.Fatalf("want 4 calls rebuilt, got %d (%v)", n, err)
	}
	if again, err := BackfillToolUsage(d); err != nil || again != 0 {
		t.Fatalf("the backfill must run once, got %d (%v)", again, err)
	}
	var s2 int
	d.QueryRow(`SELECT COUNT(*) FROM api_calls WHERE session_id='s2'`).Scan(&s2)
	if s2 != 1 {
		t.Fatalf("a session with live rows must not be touched: %d rows", s2)
	}

	// the calls keep their time, their project, and the turn each one opens
	var first []int
	rows, _ := d.Query(`SELECT first_in_turn FROM api_calls WHERE session_id='s1' ORDER BY rowid`)
	for rows.Next() {
		var f int
		rows.Scan(&f)
		first = append(first, f)
	}
	rows.Close()
	if fmt.Sprint(first) != "[1 0 1 0]" {
		t.Fatalf("first_in_turn = %v, want [1 0 1 0]", first)
	}
	var at, project string
	d.QueryRow(`SELECT created_at, project_id FROM api_calls WHERE session_id='s1' ORDER BY rowid LIMIT 1`).Scan(&at, &project)
	// the driver may drop the zero milliseconds of a DATETIME column: compare the instant
	if got, err := time.Parse(time.RFC3339Nano, at); err != nil || !got.Equal(base.Add(time.Second)) || project != "p1" {
		t.Fatalf("created_at %q (%v) project %q", at, err, project)
	}

	costs, err := ToolCosts(d, base.Add(-time.Hour), "p1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ToolCostRow{}
	for _, r := range costs {
		got[r.Tool] = r
	}
	// growth 1450-1000-80 = 370, split equally (the results' sizes were not kept); Bash: 2415-2350-5... see below
	if got["Read"].ResultTokens != 185 || got["Grep"].ResultTokens != 185 {
		t.Fatalf("Read %+v | Grep %+v", got["Read"], got["Grep"])
	}
	// call 3 context 900+1450 = 2350, call 4 context 65+2350 = 2415: 2415-2350-5 = 60
	if got["Bash"].ResultTokens != 60 {
		t.Fatalf("Bash %+v", got["Bash"])
	}
	if got["Read"].Written1h != 185 {
		t.Fatalf("the 1h split must survive: %+v", got["Read"])
	}
}
