package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// CredentialStore defines the interface for storing and retrieving session credentials.
type CredentialStore interface {
	SaveCredentials(sessionID, username, password string) error
	GetCredentials(sessionID string) (username, password string, err error)
}

// SaveCredentialsArgs represents arguments for save_credentials tool.
type SaveCredentialsArgs struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// GetCredentialsResult represents the result of get_credentials tool.
type GetCredentialsResult struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// NewSaveCredentialsTool creates the save_credentials tool definition.
func NewSaveCredentialsTool() Tool {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"username": map[string]interface{}{
				"type":        "string",
				"description": "Username for the game account",
			},
			"password": map[string]interface{}{
				"type":        "string",
				"description": "Password for the game account",
			},
		},
		"required": []string{"username", "password"},
	}

	schemaJSON, _ := json.Marshal(schema)

	return Tool{
		Name:        "save_credentials",
		Description: "Save game credentials (username and password) for the current session. Credentials are stored securely and scoped to this session only.",
		InputSchema: schemaJSON,
	}
}

// NewGetCredentialsTool creates the get_credentials tool definition.
func NewGetCredentialsTool() Tool {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}

	schemaJSON, _ := json.Marshal(schema)

	return Tool{
		Name:        "get_credentials",
		Description: "Retrieve saved game credentials (username and password) for the current session. Returns empty strings if no credentials are saved.",
		InputSchema: schemaJSON,
	}
}

// MakeSaveCredentialsHandler creates a handler for save_credentials tool.
func MakeSaveCredentialsHandler(store CredentialStore, sessionID string) ToolHandler {
	return func(ctx context.Context, arguments json.RawMessage) (*ToolResult, error) {
		var args SaveCredentialsArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Invalid arguments: %v", err)}},
				IsError: true,
			}, nil
		}

		if args.Username == "" {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: "Username cannot be empty"}},
				IsError: true,
			}, nil
		}

		if args.Password == "" {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: "Password cannot be empty"}},
				IsError: true,
			}, nil
		}

		if err := store.SaveCredentials(sessionID, args.Username, args.Password); err != nil {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to save credentials: %v", err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Credentials saved successfully for user '%s'", args.Username)}},
			IsError: false,
		}, nil
	}
}

// MakeGetCredentialsHandler creates a handler for get_credentials tool.
func MakeGetCredentialsHandler(store CredentialStore, sessionID string) ToolHandler {
	return func(ctx context.Context, arguments json.RawMessage) (*ToolResult, error) {
		username, password, err := store.GetCredentials(sessionID)
		if err != nil {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to retrieve credentials: %v", err)}},
				IsError: true,
			}, nil
		}

		if username == "" && password == "" {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: "No credentials saved for this session"}},
				IsError: false,
			}, nil
		}

		result := GetCredentialsResult{
			Username: username,
			Password: password,
		}

		resultJSON, err := json.Marshal(result)
		if err != nil {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Failed to format credentials: %v", err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []ContentBlock{{Type: "text", Text: string(resultJSON)}},
			IsError: false,
		}, nil
	}
}
