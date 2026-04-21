package executor

import (
	"encoding/json"
	"fmt"
	"log"
)

// ContextCategory is one colored segment of the context window bar.
type ContextCategory struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
	Color  string `json:"color"`
}

// ContextUsage holds a parsed get_context_usage response.
type ContextUsage struct {
	Categories  []ContextCategory `json:"categories"`
	TotalTokens int               `json:"totalTokens"`
	MaxTokens   int               `json:"maxTokens"`
	Percentage  int               `json:"percentage"`
	Model       string            `json:"model"`
}

// ParseContextUsage extracts a ContextUsage from a raw control_response JSON line.
func ParseContextUsage(raw json.RawMessage) (*ContextUsage, error) {
	var outer struct {
		Response struct {
			Subtype  string          `json:"subtype"`
			Response json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("parse control_response outer: %w", err)
	}
	if outer.Response.Subtype != "success" {
		return nil, fmt.Errorf("control_response not success: %s", outer.Response.Subtype)
	}

	var cu ContextUsage
	if err := json.Unmarshal(outer.Response.Response, &cu); err != nil {
		return nil, fmt.Errorf("parse context usage body: %w", err)
	}

	log.Printf("[executor/context] totalTokens=%d maxTokens=%d pct=%d%% model=%s",
		cu.TotalTokens, cu.MaxTokens, cu.Percentage, cu.Model)
	return &cu, nil
}

// RequestContextUsage sends a get_context_usage SDK control request to the process stdin.
func RequestContextUsage(ps *ProcessState) error {
	return SendControlRequest(ps, "get_context_usage")
}
