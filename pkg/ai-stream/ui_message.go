package aistream

import "time"

const (
	ToolStateApprovalRequested = "approval-requested"
	ToolStateApprovalResponded = "approval-responded"
)

type UIMessage struct {
	ID        string        `json:"id"`
	Role      string        `json:"role"`
	Parts     []MessagePart `json:"parts"`
	CreatedAt *time.Time    `json:"createdAt,omitempty"`
}

type MessagePart map[string]any
