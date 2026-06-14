package tools

import (
	"context"

	"github.com/hezidatong/to-tiny-claw/internal/schema"
)

type Registry interface {
	GetAvailableTools() []schema.ToolDefinition

	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}
