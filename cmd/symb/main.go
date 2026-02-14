package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/config"
	"github.com/xonecas/symb/internal/lsp"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/mcp_tools"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/store"
	"github.com/xonecas/symb/internal/tui"
)

func main() {
	// Configure zerolog to write to file (TUI uses stdout)
	if err := setupFileLogging(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to setup logging: %v\n", err)
	}
	// Load configuration
	configPath := filepath.Join(".", "config.toml")
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Load credentials
	creds, err := config.LoadCredentials()
	if err != nil {
		fmt.Printf("Error loading credentials: %v\n", err)
		os.Exit(1)
	}

	// Create provider registry
	registry := provider.NewRegistry()

	// Register all providers from config
	for name, providerCfg := range cfg.Providers {
		var factory provider.ProviderFactory

		if providerCfg.APIKeyName != "" {
			apiKey := creds.GetAPIKey(providerCfg.APIKeyName)
			factory = provider.NewOpenCodeFactory(name, providerCfg.Endpoint, apiKey)
		} else {
			factory = provider.NewOllamaFactory(name, providerCfg.Endpoint)
		}

		registry.RegisterFactory(name, factory)
	}

	// Get default provider
	providerName := cfg.DefaultProvider
	if providerName == "" {
		providers := registry.List()
		if len(providers) == 0 {
			fmt.Println("Error: No providers configured")
			os.Exit(1)
		}
		providerName = providers[0]
	}

	providerCfg, ok := cfg.Providers[providerName]
	if !ok {
		fmt.Printf("Error: Provider %q not found\n", providerName)
		os.Exit(1)
	}

	// Create provider
	prov, err := registry.Create(providerName, providerCfg.Model, providerCfg.Temperature)
	if err != nil {
		fmt.Printf("Error creating provider: %v\n", err)
		os.Exit(1)
	}
	defer prov.Close()

	// Create MCP proxy
	var mcpClient mcp.UpstreamClient
	if cfg.MCP.Upstream != "" {
		mcpClient = mcp.NewClient(cfg.MCP.Upstream)
	}

	proxy := mcp.NewProxy(mcpClient)
	if err := proxy.Initialize(context.Background()); err != nil {
		fmt.Printf("Warning: MCP init failed: %v\n", err)
	}
	defer proxy.Close()

	// Create LSP manager
	lspManager := lsp.NewManager()
	defer lspManager.StopAll(context.Background())

	// Register local tools
	fileTracker := mcp_tools.NewFileReadTracker()

	openForUserTool := mcp_tools.NewOpenForUserTool()
	openForUserHandler := mcp_tools.NewOpenForUserHandler(fileTracker, lspManager)
	proxy.RegisterTool(openForUserTool, openForUserHandler.Handle)

	grepTool := mcp_tools.NewGrepTool()
	grepHandler := mcp_tools.MakeGrepHandler()
	proxy.RegisterTool(grepTool, grepHandler)

	editTool := mcp_tools.NewEditTool()
	editHandler := mcp_tools.NewEditHandler(fileTracker, lspManager)
	proxy.RegisterTool(editTool, editHandler.Handle)

	// Open web cache (SQLite-backed, persisted across sessions).
	// If data dir or db open fails, webCache stays nil — handlers degrade gracefully.
	var webCache *store.Cache
	if cacheDir, err := config.EnsureDataDir(); err != nil {
		fmt.Printf("Warning: cache dir failed: %v\n", err)
	} else {
		cacheTTL := time.Duration(cfg.Cache.CacheTTLOrDefault()) * time.Hour
		webCache, err = store.Open(filepath.Join(cacheDir, "cache.db"), cacheTTL)
		if err != nil {
			fmt.Printf("Warning: cache open failed: %v\n", err)
		}
	}
	if webCache != nil {
		defer webCache.Close()
	}

	// Register git tools
	gitStatusTool := mcp_tools.NewGitStatusTool()
	proxy.RegisterTool(gitStatusTool, mcp_tools.MakeGitStatusHandler())

	gitDiffTool := mcp_tools.NewGitDiffTool()
	proxy.RegisterTool(gitDiffTool, mcp_tools.MakeGitDiffHandler())

	// Register web tools
	webFetchTool := mcp_tools.NewWebFetchTool()
	webFetchHandler := mcp_tools.MakeWebFetchHandler(webCache)
	proxy.RegisterTool(webFetchTool, webFetchHandler)

	exaKey := creds.GetAPIKey("exa_ai")
	webSearchTool := mcp_tools.NewWebSearchTool()
	webSearchHandler := mcp_tools.MakeWebSearchHandler(webCache, exaKey, "")
	proxy.RegisterTool(webSearchTool, webSearchHandler)

	// List all tools (local + upstream)
	tools, err := proxy.ListTools(context.Background())
	if err != nil {
		fmt.Printf("Warning: Failed to list tools: %v\n", err)
		tools = []mcp.Tool{}
	}

	// Create the BubbleTea program with model-specific prompt
	p := tea.NewProgram(
		tui.New(prov, proxy, tools, providerCfg.Model),
		tea.WithFilter(tui.MouseEventFilter),
	)

	// Set program reference for tools that need it
	openForUserHandler.SetProgram(p)
	editHandler.SetProgram(p)

	// Wire LSP diagnostics callback to TUI
	lspManager.SetCallback(func(absPath string, lines map[int]int) {
		p.Send(tui.LSPDiagnosticsMsg{FilePath: absPath, Lines: lines})
	})

	// Run the program
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running symb: %v\n", err)
		os.Exit(1)
	}
}

func setupFileLogging() error {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}

	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return err
	}

	logFile := filepath.Join(logDir, "symb.log")
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	log.Logger = log.Output(file)
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	return nil
}
