package api

import (
	"errors"
	"fmt"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/taskmcp"
)

// DefaultTaskInfoFields is what task_info returns when no fields are asked for.
var defaultTaskInfoFields = []string{"title", "description", "status", "agent"}

const recentCommentsInInfo = 5

// taskInfo describes a task to an agent so it can understand what a message or a
// teammate's work refers to. taskID defaults to the caller's own task. Only tasks
// of the caller's project are visible. fields limits the answer; empty means the
// defaults (title, description, status, agent).
func (h *Handler) taskInfo(caller dbpkg.Session, taskID string, fields []string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = caller.CardID
	}
	card, err := dbpkg.GetKanbanCard(h.db, taskID)
	if err != nil || card.ProjectID != caller.ProjectID {
		return "", fmt.Errorf("task %q not found in this project", taskID)
	}

	if len(fields) == 0 {
		fields = defaultTaskInfoFields
	}
	valid := map[string]bool{}
	for _, f := range taskmcp.TaskInfoFields {
		valid[f] = true
	}
	var want []string
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if !valid[f] {
			return "", fmt.Errorf("unknown field %q; valid fields: %s", f, strings.Join(taskmcp.TaskInfoFields, ", "))
		}
		want = append(want, f)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "task: %s\n", card.ID)
	for _, f := range want {
		switch f {
		case "title":
			fmt.Fprintf(&sb, "title: %s\n", card.Title)
		case "description":
			desc := strings.TrimSpace(card.Description)
			if desc == "" {
				desc = "(none)"
			}
			fmt.Fprintf(&sb, "description:\n%s\n", indent(desc))
		case "status":
			if list, err := dbpkg.GetKanbanList(h.db, card.ListID); err == nil {
				fmt.Fprintf(&sb, "status: %s\n", list.Name)
			}
			if card.Archived {
				sb.WriteString("archived: yes\n")
			}
		case "type":
			fmt.Fprintf(&sb, "type: %s\n", card.CardType)
		case "tags":
			fmt.Fprintf(&sb, "tags: %s\n", orNone(strings.Join(card.Tags, ", ")))
		case "agent":
			name := "(unassigned)"
			if card.AgentID != "" {
				if a, err := dbpkg.GetAgent(h.db, card.AgentID); err == nil {
					name = a.Name
				}
			}
			fmt.Fprintf(&sb, "agent: %s\n", name)
		case "dependencies":
			var lines []string
			for _, id := range card.DependsOn {
				dep, err := dbpkg.GetKanbanCard(h.db, id)
				if err != nil {
					continue
				}
				lines = append(lines, fmt.Sprintf("- %s [%s] (task %s)", dep.Title, h.listName(dep.ListID), dep.ID))
			}
			fmt.Fprintf(&sb, "dependencies:\n%s\n", orNone(strings.Join(lines, "\n")))
		case "parent":
			parent := "(none)"
			if card.ParentID != "" {
				if p, err := dbpkg.GetKanbanCard(h.db, card.ParentID); err == nil {
					parent = fmt.Sprintf("%s (task %s)", p.Title, p.ID)
				}
			}
			fmt.Fprintf(&sb, "parent: %s\n", parent)
		case "dates":
			fmt.Fprintf(&sb, "start: %s\nend: %s\n", orNone(card.StartDate), orNone(card.EndDate))
		case "comments":
			comments, _ := dbpkg.ListCardComments(h.db, card.ID)
			if len(comments) > recentCommentsInInfo {
				comments = comments[len(comments)-recentCommentsInInfo:]
			}
			var lines []string
			for _, c := range comments {
				lines = append(lines, fmt.Sprintf("- [%s] %s: %s", c.Kind, c.Author, c.Body))
			}
			fmt.Fprintf(&sb, "comments (latest %d):\n%s\n", recentCommentsInInfo, orNone(strings.Join(lines, "\n")))
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (h *Handler) listName(listID string) string {
	if l, err := dbpkg.GetKanbanList(h.db, listID); err == nil {
		return l.Name
	}
	return "?"
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

var errNoAgent = errors.New("this session has no agent")
