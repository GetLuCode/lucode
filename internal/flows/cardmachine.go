package flows

import "strings"

// The lifecycle of a card: where it may go and on what condition. The hub applies it
// (api.moveBlocker) and serves it in XState's JSON format, so the web loads the same
// rules with createMachine instead of repeating them.
//
// A card may move freely between lists, as on any board. The only guarded destination
// is In Progress, because entering it starts an agent.

// CardStates are the workflow roles of a list; "custom" is a list without a role.
var CardStates = []string{"backlog", "todo", "inprogress", "review", "done", "blocked", "custom"}

// Guards of the card machine.
const (
	GuardCanStart   = "canStart"   // it has an agent
	GuardCanRestart = "canRestart" // it has an agent, its dependencies are done and its blocker was answered
)

// CardGuardContext are the facts the guards look at.
type CardGuardContext struct {
	HasAgent        bool `json:"hasAgent"`
	DepsDone        bool `json:"depsDone"`
	BlockerAnswered bool `json:"blockerAnswered"`
}

// CardState maps a list's kind to its state in the machine.
func CardState(kind string) string {
	if kind == "" {
		return "custom"
	}
	return kind
}

// CardGuard returns the guard of a move between two states ("" = always allowed).
func CardGuard(from, to string) string {
	if to != "inprogress" {
		return ""
	}
	if from == "blocked" {
		return GuardCanRestart
	}
	return GuardCanStart
}

// CardGuardPasses evaluates a guard.
func CardGuardPasses(guard string, c CardGuardContext) bool {
	switch guard {
	case GuardCanStart:
		return c.HasAgent
	case GuardCanRestart:
		return c.HasAgent && c.DepsDone && c.BlockerAnswered
	}
	return true
}

// CardMoveEvent is the machine's event for moving a card to a state.
func CardMoveEvent(to string) string { return "MOVE_" + strings.ToUpper(to) }

// CardMachine is the lifecycle as an XState machine config. The initial state is only
// a default: a real card is restored into the state of its current list.
func CardMachine() map[string]any {
	states := map[string]any{}
	for _, from := range CardStates {
		on := map[string]any{}
		for _, to := range CardStates {
			t := map[string]any{"target": to}
			if g := CardGuard(from, to); g != "" {
				t["guard"] = g
			}
			on[CardMoveEvent(to)] = t
		}
		states[from] = map[string]any{"on": on}
	}
	return map[string]any{"id": "card", "initial": "backlog", "states": states}
}
