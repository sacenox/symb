# Symb Tools Documentation

This document provides comprehensive details on Symb's tool system, including architecture, implementation, built-in tools, MCP integration, and development guides.

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Tool Types](#tool-types)
3. [Built-in Tools](#built-in-tools)
4. [MCP Integration](#mcp-integration)
5. [Tool Development Guide](#tool-development-guide)
6. [Security Model](#security-model)
7. [Configuration](#configuration)
8. [Testing Tools](#testing-tools)
9. [Performance Considerations](#performance-considerations)
10. [Troubleshooting](#troubleshooting)

---

## Architecture Overview

### System Design

Symb implements a **layered tool architecture** with MCP (Model Context Protocol) at its core:

```
┌─────────────────────────────────────────────────────────┐
│                    LLM Providers                         │
│         (Ollama, OpenCode, etc.)                        │
└────────────────┬────────────────────────────────────────┘
                 │ ChatWithTools()
┌────────────────▼────────────────────────────────────────┐
│              LLM Loop (internal/llm)                     │
│  - ProcessTurn: orchestrates tool calls                 │
│  - Max 20 rounds per turn                               │
│  - Sequential tool execution                            │
└────────────────┬────────────────────────────────────────┘
                 │ CallTool()
┌────────────────▼────────────────────────────────────────┐
│            MCP Proxy (internal/mcp)                      │
│  - Routes to local or upstream tools                    │
│  - Retry logic for rate limits                          │
│  - Error handling & normalization                       │
└────┬───────────────────────────────────────────┬────────┘
     │                                           │
     │ Local Tools                               │ Upstream MCP
     ▼                                           ▼
┌────────────────┐                    ┌─────────────────┐
│  Open Tool     │                    │ MCP Client      │
│  Grep Tool     │                    │ (HTTP/SSE)      │
└────────────────┘                    └─────────────────┘
```

---

## Tool Types

### 1. Local Tools

**Definition**: Tools implemented directly in Symb's codebase.

**Location**: `internal/mcp_tools/`

**Registration**: `cmd/symb/main.go`

**Current Local Tools**:

- `Open` - File viewer with syntax highlighting
- `Grep` - File/content search

**Characteristics**:

- ✓ No network latency
- ✓ Full control over implementation
- ✓ Direct TUI integration
- ✓ Custom security policies

### 2. Upstream MCP Tools

**Definition**: Tools provided by external MCP servers via HTTP/SSE.

**Protocol**: Model Context Protocol (MCP)

**Configuration**: `config.toml` → `[mcp]` section

**Characteristics**:

- ✓ Hot-pluggable (no code changes)
- ✓ Leverage existing MCP ecosystem
- ✓ Can call external APIs/services
- ✗ Network latency
- ✗ Requires upstream server availability
- ✗ Limited security control

### Retry Logic

Handles rate limits and transient failures:

```go
// Retry delays: 2s, 5s, 10s
var toolRetryDelays = []time.Duration{
    2 * time.Second,
    5 * time.Second,
    10 * time.Second,
}

func (p *Proxy) callUpstreamWithRetry(ctx context.Context, name string, args json.RawMessage) (*ToolResult, error) {
    var lastErr error

    for attempt := 0; attempt <= len(toolRetryDelays); attempt++ {
        result, err := p.upstream.CallTool(ctx, name, args)
        if err == nil {
            return result, nil
        }

        lastErr = err

        // Check if we should retry
        if !shouldRetry(err) {
            return nil, err
        }

        // Check for Retry-After header (429 responses)
        if retryAfter, ok := parseRetryAfter(err); ok {
            delay = retryAfter
        } else {
            delay = toolRetryDelays[attempt]
        }

        // Wait with context cancellation support
        select {
        case <-time.After(delay):
            continue
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }

    return nil, fmt.Errorf("retry exhausted after %d attempts: %w", len(toolRetryDelays)+1, lastErr)
}
```

**Features**:

- Respects `Retry-After` HTTP headers
- Exponential backoff (2s → 5s → 10s)
- Context-aware (cancellation support)
- Logs retry attempts
- Returns meaningful error after exhaustion

---

## Tool Development Guide

### Creating a Local Tool

**Step 1: Define the tool schema**

```go
// internal/mcp_tools/mytool.go
package mcp_tools

import (
    "context"
    "encoding/json"
    "github.com/xonecas/symb/internal/mcp"
)

func NewMyTool() mcp.Tool {
    schema := map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "param1": map[string]interface{}{
                "type":        "string",
                "description": "First parameter description",
            },
            "param2": map[string]interface{}{
                "type":        "integer",
                "description": "Second parameter (optional)",
            },
        },
        "required": []string{"param1"},
    }

    schemaJSON, _ := json.Marshal(schema)

    return mcp.Tool{
        Name:        "MyTool",
        Description: "Does something useful with param1 and optionally param2",
        InputSchema: schemaJSON,
    }
}
```

**Step 2: Implement the handler**

```go
type MyToolArgs struct {
    Param1 string `json:"param1"`
    Param2 int    `json:"param2,omitempty"`
}

func MakeMyToolHandler() mcp.ToolHandler {
    return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
        var args MyToolArgs
        if err := json.Unmarshal(arguments, &args); err != nil {
            return &mcp.ToolResult{
                Content: []mcp.ContentBlock{{
                    Type: "text",
                    Text: "Invalid arguments: " + err.Error(),
                }},
                IsError: true,
            }, nil
        }

        // Validate inputs
        if args.Param1 == "" {
            return &mcp.ToolResult{
                Content: []mcp.ContentBlock{{
                    Type: "text",
                    Text: "param1 cannot be empty",
                }},
                IsError: true,
            }, nil
        }

        // Execute tool logic
        result := doSomething(args.Param1, args.Param2)

        // Return success
        return &mcp.ToolResult{
            Content: []mcp.ContentBlock{{
                Type: "text",
                Text: result,
            }},
            IsError: false,
        }, nil
    }
}

func doSomething(param1 string, param2 int) string {
    // Your tool implementation here
    return "Result from MyTool"
}
```

**Step 3: Register the tool**

```go
// cmd/symb/main.go
func main() {
    // ... existing code ...

    // Create MCP proxy
    proxy := mcp.NewProxy(mcpClient)
    proxy.Initialize(context.Background())

    // Register existing tools
    proxy.RegisterTool(mcp_tools.NewOpenForUserTool(), mcp_tools.NewOpenForUserHandler().Handle)
    proxy.RegisterTool(mcp_tools.NewGrepTool(), mcp_tools.MakeGrepHandler())

    // Register your new tool
    proxy.RegisterTool(mcp_tools.NewMyTool(), mcp_tools.MakeMyToolHandler())

    // ... rest of main ...
}
```

**Step 4: Update system prompts**

Add tool description to all prompt files (`internal/llm/*.md`):

````markdown
### `MyTool`

Does something useful with param1 and optionally param2.

**Parameters:**

```json
{
  "param1": "string (required)",
  "param2": 123 // optional integer
}
```
````

**Example:**

```json
{ "param1": "example", "param2": 42 }
```

````

**Step 5: Test the tool**

```go
// internal/mcp_tools/mytool_test.go
package mcp_tools

import (
    "context"
    "encoding/json"
    "testing"
)

func TestMyTool(t *testing.T) {
    handler := MakeMyToolHandler()

    tests := []struct {
        name    string
        args    map[string]interface{}
        wantErr bool
        wantText string
    }{
        {
            name: "valid input",
            args: map[string]interface{}{
                "param1": "test",
                "param2": 42,
            },
            wantErr: false,
            wantText: "Result from MyTool",
        },
        {
            name: "missing required param",
            args: map[string]interface{}{},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            argsJSON, _ := json.Marshal(tt.args)

            result, err := handler(context.Background(), argsJSON)
            if err != nil {
                t.Fatalf("unexpected error: %v", err)
            }

            if result.IsError != tt.wantErr {
                t.Errorf("IsError = %v, want %v", result.IsError, tt.wantErr)
            }

            if !tt.wantErr && result.Content[0].Text != tt.wantText {
                t.Errorf("Text = %q, want %q", result.Content[0].Text, tt.wantText)
            }
        })
    }
}
````

### TUI Integration

If your tool needs to update the TUI:

**Step 1: Define a message type**

```go
// internal/mcp_tools/mytool.go
type MyToolMsg struct {
    Data string
}
```

**Step 2: Send message from handler**

```go
func MakeMyToolHandler(updateChan chan<- interface{}) mcp.ToolHandler {
    return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
        // ... handler logic ...

        // Send message to TUI
        if updateChan != nil {
            updateChan <- MyToolMsg{Data: "something"}
        }

        return result, nil
    }
}
```

**Step 3: Handle message in TUI**

```go
// internal/tui/tui.go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case mcp_tools.MyToolMsg:
        // Update model state based on message
        m.someField = msg.Data
        return m, nil
    // ... other cases ...
    }
}
```

---

## References

**Standards**:

- [Model Context Protocol Specification](https://modelcontextprotocol.io/)
- [JSON Schema](https://json-schema.org/)

**Inspiration**:

- OpenCode MCP implementation
- Anthropic Claude tool use
- OpenAI function calling

**Related Docs**:

- `docs/PROMPTS.md` - System prompt documentation
- `docs/DESIGN.md` - Architecture overview
- `docs/TUI_TESTING.md` - TUI testing guide
- `internal/llm/loop.go` - LLM interaction loop
- `internal/mcp/proxy.go` - MCP proxy implementation
