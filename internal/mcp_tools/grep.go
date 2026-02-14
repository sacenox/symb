// Package mcp_tools provides local MCP tool implementations.
package mcp_tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/xonecas/symb/internal/filesearch"
	"github.com/xonecas/symb/internal/mcp"
)

// GrepArgs represents arguments for the grep tool.
type GrepArgs struct {
	Pattern       string `json:"pattern"`                  // Pattern to search for (regex)
	ContentSearch bool   `json:"content_search,omitempty"` // Search file contents (default: false, searches filenames)
	MaxResults    int    `json:"max_results,omitempty"`    // Max results to return (default: 100)
	CaseSensitive bool   `json:"case_sensitive,omitempty"` // Case-sensitive matching (default: false)
}

// NewGrepTool creates the grep tool definition.
func NewGrepTool() mcp.Tool {
	return mcp.Tool{
		Name:        "Grep",
		Description: "Search for files by name (fuzzy) or search file contents (grep). Respects .gitignore. Use content_search=false for finding files, content_search=true for searching content.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern":        {"type": "string", "description": "Pattern to search for (regex). For filenames: matches against basename or path. For content: matches line contents."},
				"content_search": {"type": "boolean", "description": "If true, search file contents (grep); if false, search filenames (find). Default: false"},
				"max_results":    {"type": "integer", "description": "Maximum number of results to return. Default: 100"},
				"case_sensitive": {"type": "boolean", "description": "Enable case-sensitive matching. Default: false (case-insensitive)"}
			},
			"required": ["pattern"]
		}`),
	}
}

// MakeGrepHandler creates a handler for the grep tool.
func MakeGrepHandler() mcp.ToolHandler {
	return func(ctx context.Context, arguments json.RawMessage) (*mcp.ToolResult, error) {
		var args GrepArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid arguments: %v", err)}},
				IsError: true,
			}, nil
		}

		if args.Pattern == "" {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: "Pattern cannot be empty"}},
				IsError: true,
			}, nil
		}

		// Default max results
		if args.MaxResults <= 0 {
			args.MaxResults = 100
		}

		// Get current working directory
		cwd, err := os.Getwd()
		if err != nil {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to get working directory: %v", err)}},
				IsError: true,
			}, nil
		}

		// Create searcher
		searcher, err := filesearch.NewSearcher(cwd)
		if err != nil {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to create searcher: %v", err)}},
				IsError: true,
			}, nil
		}

		// Execute search
		results, err := searcher.Search(ctx, filesearch.Options{
			Pattern:       args.Pattern,
			ContentSearch: args.ContentSearch,
			MaxResults:    args.MaxResults,
			CaseSensitive: args.CaseSensitive,
			RootDir:       cwd,
		})

		if err != nil {
			return &mcp.ToolResult{
				Content: []mcp.ContentBlock{{Type: "text", Text: fmt.Sprintf("Search failed: %v", err)}},
				IsError: true,
			}, nil
		}

		// Format results
		var output strings.Builder
		if len(results) == 0 {
			output.WriteString("No matches found")
		} else {
			if args.ContentSearch {
				output.WriteString(fmt.Sprintf("Found %d match(es):\n\n", len(results)))
				for _, r := range results {
					output.WriteString(fmt.Sprintf("%s:%d:%s\n", r.Path, r.Line, r.Content))
				}
			} else {
				output.WriteString(fmt.Sprintf("Found %d file(s):\n\n", len(results)))
				for _, r := range results {
					output.WriteString(fmt.Sprintf("%s\n", r.Path))
				}
			}

			if len(results) >= args.MaxResults {
				output.WriteString(fmt.Sprintf("\n(Limited to %d results. Use max_results parameter to see more)", args.MaxResults))
			}
		}

		return &mcp.ToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: output.String()}},
			IsError: false,
		}, nil
	}
}
