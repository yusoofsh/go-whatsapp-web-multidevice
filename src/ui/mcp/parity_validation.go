package mcp

import (
	"fmt"
	"strings"

	mcpg "github.com/mark3labs/mcp-go/mcp"
)

// Presence is immediate-only. Reject scheduling rather than silently sending now.
var presenceScheduleFields = [...]string{
	"scheduled_at", "timezone", "recurrence", "weekdays",
	"day_of_month", "end_at", "occurrence_limit",
}

func validatePresenceSchedule(request mcpg.CallToolRequest) error {
	for _, field := range presenceScheduleFields {
		if _, exists := request.GetArguments()[field]; exists {
			return fmt.Errorf("presence does not support %s; remove scheduling fields to apply presence immediately", field)
		}
	}
	return nil
}

// Bare numeric group IDs must never be interpreted as individual accounts.
func normalizeParityGroupID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !strings.Contains(id, "@") {
		id += "@g.us"
	}
	user, ok := strings.CutSuffix(id, "@g.us")
	if !ok || len(id) > 256 {
		return "", fmt.Errorf("group_id must be a numeric group ID or a group JID ending in @g.us")
	}
	parts := strings.Split(user, "-")
	if len(parts) > 2 {
		return "", fmt.Errorf("invalid group_id")
	}
	for _, part := range parts {
		if part == "" {
			return "", fmt.Errorf("invalid group_id")
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return "", fmt.Errorf("invalid group_id")
			}
		}
	}
	return id, nil
}
