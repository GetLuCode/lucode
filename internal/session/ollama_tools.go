package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ── Tool definitions sent to Ollama ──────────────────────────────────────────

// ollamaTool is the JSON structure for a tool declaration in the Ollama API.
type ollamaTool struct {
	Type     string          `json:"type"`
	Function ollamaToolFunc  `json:"function"`
}

type ollamaToolFunc struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ollamaToolCall is a tool call requested by the model in a response chunk.
type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// lucodeTools is the set of tools exposed to Ollama models.
// These are executed directly by the orchestrator (no hook binary needed).
var lucodeTools = []ollamaTool{
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "read_file",
			Description: "Read the full contents of a file at the given path.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Absolute or relative path to the file.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "write_file",
			Description: "Write (overwrite) a file with the given content. Creates parent directories if needed.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Path to the file to write.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Content to write to the file.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "edit_file",
			Description: "Replace an exact string in a file with a new string. Fails if old_string is not found.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Path to the file to edit.",
					},
					"old_string": map[string]interface{}{
						"type":        "string",
						"description": "Exact string to find and replace.",
					},
					"new_string": map[string]interface{}{
						"type":        "string",
						"description": "Replacement string.",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
	},
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "run_command",
			Description: "Run a shell command in the working directory and return stdout+stderr. Use for builds, tests, grep, ls, etc.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Shell command to execute (runs via sh -c).",
					},
				},
				"required": []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "list_directory",
			Description: "List files and subdirectories in a directory.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path to list.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ollamaToolFunc{
			Name:        "search_files",
			Description: "Search for a string pattern in files under a directory (recursive grep).",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Root directory to search in.",
					},
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "String or regex pattern to search for.",
					},
					"file_pattern": map[string]interface{}{
						"type":        "string",
						"description": "Optional glob to filter files (e.g. '*.go', '*.ts'). Defaults to all files.",
					},
				},
				"required": []string{"path", "pattern"},
			},
		},
	},
}

// ── Tool executor ─────────────────────────────────────────────────────────────

// executeTool runs a tool by name with the given JSON arguments.
// workDir is used as the base for relative paths and as the CWD for commands.
// Returns a human-readable result string or an error description.
func executeTool(name string, argsJSON json.RawMessage, workDir string) string {
	var args map[string]interface{}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return fmt.Sprintf("error: could not parse arguments: %v", err)
	}

	str := func(key string) string {
		v, _ := args[key].(string)
		return v
	}

	resolvePath := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(workDir, p)
	}

	switch name {
	case "read_file":
		path := resolvePath(str("path"))
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Sprintf("error reading %s: %v", path, err)
		}
		return string(data)

	case "write_file":
		path := resolvePath(str("path"))
		content := str("content")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Sprintf("error creating directories for %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Sprintf("error writing %s: %v", path, err)
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(content), path)

	case "edit_file":
		path := resolvePath(str("path"))
		oldStr := str("old_string")
		newStr := str("new_string")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Sprintf("error reading %s: %v", path, err)
		}
		original := string(data)
		if !strings.Contains(original, oldStr) {
			return fmt.Sprintf("error: old_string not found in %s", path)
		}
		replaced := strings.Replace(original, oldStr, newStr, 1)
		if err := os.WriteFile(path, []byte(replaced), 0644); err != nil {
			return fmt.Sprintf("error writing %s: %v", path, err)
		}
		return fmt.Sprintf("edited %s successfully", path)

	case "run_command":
		command := str("command")
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		result := string(out)
		if err != nil {
			if result != "" {
				return fmt.Sprintf("exit error: %v\n%s", err, result)
			}
			return fmt.Sprintf("exit error: %v", err)
		}
		if result == "" {
			return "(no output)"
		}
		// Truncate very long output
		const maxOut = 8000
		if len(result) > maxOut {
			result = result[:maxOut] + fmt.Sprintf("\n... (truncated, %d bytes total)", len(result))
		}
		return result

	case "list_directory":
		path := resolvePath(str("path"))
		entries, err := os.ReadDir(path)
		if err != nil {
			return fmt.Sprintf("error listing %s: %v", path, err)
		}
		var sb strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				sb.WriteString(e.Name() + "/\n")
			} else {
				info, _ := e.Info()
				if info != nil {
					sb.WriteString(fmt.Sprintf("%s (%d bytes)\n", e.Name(), info.Size()))
				} else {
					sb.WriteString(e.Name() + "\n")
				}
			}
		}
		if sb.Len() == 0 {
			return "(empty directory)"
		}
		return sb.String()

	case "search_files":
		path := resolvePath(str("path"))
		pattern := str("pattern")
		filePattern := str("file_pattern")

		args := []string{"-r", "--include=" + filePattern}
		if filePattern == "" {
			args = []string{"-r"}
		}
		args = append(args, pattern, path)

		cmd := exec.Command("grep", args...)
		out, _ := cmd.CombinedOutput()
		result := string(out)
		if result == "" {
			return "no matches found"
		}
		const maxOut = 4000
		if len(result) > maxOut {
			result = result[:maxOut] + "\n... (truncated)"
		}
		return result

	default:
		return fmt.Sprintf("error: unknown tool %q", name)
	}
}
