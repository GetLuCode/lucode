package session

import (
	"encoding/json"
	"testing"
)

// The hub sends the agent's worktree settings to the runner inside the turn; if
// they are dropped, claude starts in the main checkout and enters worktrees from
// origin/main (its default baseRef) instead of the local HEAD.
func TestRemoteTurnCarriesWorktreeSettings(t *testing.T) {
	req := remoteTurnRequest("sess", TurnOptions{Worktree: true, WorktreeBaseRef: "head", AutoMode: true})
	data, err := json.Marshal(RemoteTurn{Req: req})
	if err != nil {
		t.Fatal(err)
	}
	var got RemoteTurn
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Req.Worktree || got.Req.WorktreeBaseRef != "head" {
		t.Errorf("worktree settings lost on the way to the runner: %+v", got.Req)
	}
}
