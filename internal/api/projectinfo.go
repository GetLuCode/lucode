package api

import (
	"fmt"
	"sort"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
)

// projectInfo answers the project_info tool: where the session is and what the project holds.
func (h *Handler) projectInfo(sess dbpkg.Session) (string, error) {
	p, err := dbpkg.GetProject(h.db, sess.ProjectID)
	if err != nil {
		return "", fmt.Errorf("project not found: %w", err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Project: %s (id %s)\n", p.Name, p.ID)

	runner := "this instance"
	if p.RunnerID != "" {
		runner = p.RunnerID
	}
	fmt.Fprintf(&sb, "Runner: %s\n", runner)

	dirs := p.WorkDirs
	if p.RunnerID != "" {
		dirs = p.EnvDirs[p.RunnerID]
	}
	if len(dirs) == 0 {
		sb.WriteString("Work dir: none set\n")
	} else {
		fmt.Fprintf(&sb, "Work dir: %s\n", dirs[0])
		if len(dirs) > 1 {
			fmt.Fprintf(&sb, "Extra dirs: %s\n", strings.Join(dirs[1:], ", "))
		}
	}
	var others []string
	for id, d := range p.EnvDirs {
		if id != p.RunnerID && len(d) > 0 {
			others = append(others, fmt.Sprintf("%s: %s", id, strings.Join(d, ", ")))
		}
	}
	sort.Strings(others)
	if len(others) > 0 {
		fmt.Fprintf(&sb, "Dirs on other runners: %s\n", strings.Join(others, "; "))
	}

	ai := "default account"
	cfg := p.ClaudeConfigDir
	if p.AIProfileID != "" {
		if prof, err := dbpkg.GetAIProfile(h.db, p.AIProfileID); err == nil {
			ai = prof.Name
			if prof.Email != "" {
				ai += " — " + prof.Email
			}
			if p.RunnerID == "" {
				cfg = prof.ClaudeConfigDir
			}
		}
	}
	fmt.Fprintf(&sb, "AI: %s\n", ai)
	if cfg != "" {
		fmt.Fprintf(&sb, "Config dir: %s\n", cfg)
	}
	if p.DefaultModel != "" {
		fmt.Fprintf(&sb, "Default model: %s\n", p.DefaultModel)
	}

	lists, _ := dbpkg.ListKanbanLists(h.db, p.ID)
	cards, _ := dbpkg.ListKanbanCards(h.db, p.ID, false)
	count := map[string]int{}
	for _, c := range cards {
		count[c.ListID]++
	}
	var parts []string
	for _, l := range lists {
		parts = append(parts, fmt.Sprintf("%s %d", l.Name, count[l.ID]))
	}
	fmt.Fprintf(&sb, "Tasks: %d (%s)\n", len(cards), strings.Join(parts, ", "))

	agents, _ := dbpkg.ListAgents(h.db, p.ID)
	var names []string
	for _, a := range agents {
		names = append(names, a.Name)
	}
	if len(names) == 0 {
		sb.WriteString("Agents: none\n")
	} else {
		fmt.Fprintf(&sb, "Agents (%d): %s\n", len(names), strings.Join(names, ", "))
	}
	return strings.TrimSpace(sb.String()), nil
}
