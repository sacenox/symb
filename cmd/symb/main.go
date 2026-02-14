package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/xonecas/symb/internal/mcptools"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/store"
	"github.com/xonecas/symb/internal/tui"
)

func main() {
	if err := setupFileLogging(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to setup logging: %v\n", err)
	}

	cfg, err := config.Load(filepath.Join(".", "config.toml"))
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	creds, err := config.LoadCredentials()
	if err != nil {
		fmt.Printf("Error loading credentials: %v\n", err)
		os.Exit(1)
	}

	registry := buildRegistry(cfg, creds)

	providerName, providerCfg := resolveProvider(cfg, registry)

	prov, err := registry.Create(providerName, providerCfg.Model, providerCfg.Temperature)
	if err != nil {
		fmt.Printf("Error creating provider: %v\n", err)
		os.Exit(1)
	}
	defer prov.Close()

	proxy, lspManager, webCache := setupServices(cfg, creds)
	defer proxy.Close()
	defer lspManager.StopAll(context.Background())
	if webCache != nil {
		defer webCache.Close()
	}

	tools, err := proxy.ListTools(context.Background())
	if err != nil {
		fmt.Printf("Warning: Failed to list tools: %v\n", err)
		tools = []mcp.Tool{}
	}

	sessionID := newSessionID()
	if webCache != nil {
		if err := webCache.CreateSession(sessionID); err != nil {
			fmt.Printf("Warning: failed to create session: %v\n", err)
		}
	}

	p := tea.NewProgram(
		tui.New(prov, proxy, tools, providerCfg.Model, webCache, sessionID),
		tea.WithFilter(tui.MouseEventFilter),
	)
	lspManager.SetCallback(func(absPath string, lines map[int]int) {
		p.Send(tui.LSPDiagnosticsMsg{FilePath: absPath, Lines: lines})
	})

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running symb: %v\n", err)
		os.Exit(1)
	}
}

func buildRegistry(cfg *config.Config, creds *config.Credentials) *provider.Registry {
	registry := provider.NewRegistry()
	for name, providerCfg := range cfg.Providers {
		var factory provider.Factory
		if providerCfg.APIKeyName != "" {
			factory = provider.NewOpenCodeFactory(name, providerCfg.Endpoint, creds.GetAPIKey(providerCfg.APIKeyName))
		} else {
			factory = provider.NewOllamaFactory(name, providerCfg.Endpoint)
		}
		registry.RegisterFactory(name, factory)
	}
	return registry
}

func resolveProvider(cfg *config.Config, registry *provider.Registry) (string, config.ProviderConfig) {
	name := cfg.DefaultProvider
	if name == "" {
		providers := registry.List()
		if len(providers) == 0 {
			fmt.Println("Error: No providers configured")
			os.Exit(1)
		}
		name = providers[0]
	}
	pcfg, ok := cfg.Providers[name]
	if !ok {
		fmt.Printf("Error: Provider %q not found\n", name)
		os.Exit(1)
	}
	return name, pcfg
}

func setupServices(cfg *config.Config, creds *config.Credentials) (*mcp.Proxy, *lsp.Manager, *store.Cache) {
	var mcpClient mcp.UpstreamClient
	if cfg.MCP.Upstream != "" {
		mcpClient = mcp.NewClient(cfg.MCP.Upstream)
	}
	proxy := mcp.NewProxy(mcpClient)
	if err := proxy.Initialize(context.Background()); err != nil {
		fmt.Printf("Warning: MCP init failed: %v\n", err)
	}

	lspManager := lsp.NewManager()
	fileTracker := mcptools.NewFileReadTracker()

	readHandler := mcptools.NewReadHandler(fileTracker, lspManager)
	proxy.RegisterTool(mcptools.NewReadTool(), readHandler.Handle)

	proxy.RegisterTool(mcptools.NewGrepTool(), mcptools.MakeGrepHandler())

	editHandler := mcptools.NewEditHandler(fileTracker, lspManager)
	proxy.RegisterTool(mcptools.NewEditTool(), editHandler.Handle)

	webCache := openWebCache(cfg)

	proxy.RegisterTool(mcptools.NewWebFetchTool(), mcptools.MakeWebFetchHandler(webCache))

	exaKey := creds.GetAPIKey("exa_ai")
	proxy.RegisterTool(mcptools.NewWebSearchTool(), mcptools.MakeWebSearchHandler(webCache, exaKey, ""))

	return proxy, lspManager, webCache
}

func openWebCache(cfg *config.Config) *store.Cache {
	cacheDir, err := config.EnsureDataDir()
	if err != nil {
		fmt.Printf("Warning: cache dir failed: %v\n", err)
		return nil
	}
	cacheTTL := time.Duration(cfg.Cache.CacheTTLOrDefault()) * time.Hour
	cache, err := store.Open(filepath.Join(cacheDir, "cache.db"), cacheTTL)
	if err != nil {
		fmt.Printf("Warning: cache open failed: %v\n", err)
		return nil
	}
	return cache
}

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
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
