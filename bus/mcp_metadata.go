package bus

import (
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Hints describe effects, not authorization. The same tools serve local and
// hosted workspaces, and their normal permission checks always apply.
func busTool(name, description string) *mcp.Tool {
	no, open := false, false
	a := &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &open}
	switch name {
	case "list_peers", "message_receipt", "list_tasks", "list_task_progress", "get_node_status", "list_channels", "read_channel", "list_attachments":
		a.ReadOnlyHint, a.IdempotentHint = true, true
	case "acknowledge_messages", "subscribe_channel":
		a.IdempotentHint = true
	case "message_peer", "post_channel", "ask_user", "publish_output":
		open = true // These can notify another agent or person.
	}
	// check_inbox commits delivery, and read_attachment accounts for transferred
	// bytes. Neither is a read-only or idempotent operation.
	title := strings.ReplaceAll(name, "_", " ")
	title = strings.ToUpper(title[:1]) + title[1:]
	t := &mcp.Tool{Name: name, Title: title, Description: description, Annotations: a}
	if name == "check_inbox" {
		t.InputSchema = map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"limit":  map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "Maximum messages; 0 or omitted uses the default of 50."},
				"waitMs": map[string]any{"type": "integer", "minimum": 0, "maximum": 25000, "description": "Wait for new messages, in milliseconds; 0 returns immediately."},
			},
		}
	}
	return t
}
