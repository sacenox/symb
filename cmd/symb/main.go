package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xonecas/symb/internal/config"
	"github.com/xonecas/symb/internal/delta"
	"github.com/xonecas/symb/internal/lsp"
	"github.com/xonecas/symb/internal/mcp"
	"github.com/xonecas/symb/internal/mcptools"
	"github.com/xonecas/symb/internal/provider"
	"github.com/xonecas/symb/internal/shell"
	"github.com/xonecas/symb/internal/store"
	"github.com/xonecas/symb/internal/treesitter"
	"github.com/xonecas/symb/internal/tui"
)

func main() {
	if err := setupFileLogging(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to setup logging: %v\n", err)
	}

	// Parse CLI flags.
	flagSession := flag.String("s", "", "resume a session by ID")
	flagList := flag.Bool("l", false, "list sessions")
	flagContinue := flag.Bool("c", false, "continue most recent session")
	flag.StringVar(flagSession, "session", "", "resume a session by ID")
	flag.BoolVar(flagList, "list", false, "list sessions")
	flag.BoolVar(flagContinue, "continue", false, "continue most recent session")
	flag.Parse()

	configPath := filepath.Join(".", "config.toml")
	if dataDir, err := config.DataDir(); err == nil {
		dataDirPath := filepath.Join(dataDir, "config.toml")
		if _, err := os.Stat(dataDirPath); err == nil {
			configPath = dataDirPath
		}
	}
	cfg, err := config.Load(configPath)
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

	prov, err := registry.Create(providerName, providerCfg.Model, provider.Options{
		Temperature: providerCfg.Temperature,
	})
	if err != nil {
		fmt.Printf("Error creating provider: %v\n", err)
		os.Exit(1)
	}
	defer prov.Close()

	svc := setupServices(cfg, creds)
	defer svc.proxy.Close()
	defer svc.lspManager.StopAll(context.Background())
	if svc.webCache != nil {
		defer svc.webCache.Close()
	}

	// Handle --list: print sessions and exit.
	if *flagList {
		listSessions(svc.webCache)
		return
	}

	tools, err := svc.proxy.ListTools(context.Background())
	if err != nil {
		fmt.Printf("Warning: Failed to list tools: %v\n", err)
		tools = []mcp.Tool{}
	}

	// Register SubAgent tool after obtaining the tools list.
	// SubAgent needs access to provider and all tools to spawn isolated sub-agents.
	subAgentHandler := mcptools.NewSubAgentHandler(
		prov,
		svc.lspManager,
		svc.deltaTracker,
		svc.shell,
		svc.webCache,
		svc.exaKey,
		tools,
	)
	svc.proxy.RegisterTool(mcptools.NewSubAgentTool(), subAgentHandler.Handle)

	// Re-fetch tools list to include SubAgent
	tools, err = svc.proxy.ListTools(context.Background())
	if err != nil {
		fmt.Printf("Warning: Failed to list tools after SubAgent registration: %v\n", err)
		tools = []mcp.Tool{}
	}

	sessionID, resumeHistory := resolveSession(*flagSession, *flagContinue, svc.webCache)

	// Build tree-sitter project symbol index.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("Warning: failed to get working directory: %v\n", err)
		cwd = "."
	}
	tsIndex := treesitter.NewIndex(cwd)
	if err := tsIndex.Build(); err != nil {
		log.Warn().Err(err).Msg("tree-sitter index build failed")
	}

	// Wire index into Read/Edit handlers for incremental updates.
	svc.readHandler.SetTSIndex(tsIndex)
	svc.editHandler.SetTSIndex(tsIndex)

	// Set session on delta tracker so file deltas are linked.
	if svc.deltaTracker != nil {
		svc.deltaTracker.SetSession(sessionID)
	}

	p := tea.NewProgram(
		tui.New(prov, svc.proxy, tools, providerCfg.Model, svc.webCache, sessionID, tsIndex, svc.deltaTracker, svc.fileTracker, providerName, svc.scratchpad, resumeHistory),
		tea.WithFilter(tui.MouseEventFilter),
	)
	svc.lspManager.SetCallback(func(absPath string, lines map[int]int) {
		p.Send(tui.LSPDiagnosticsMsg{FilePath: absPath, Lines: lines})
	})

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running symb: %v\n", err)
		os.Exit(1)
	}
}

func buildRegistry(cfg *config.Config, _ *config.Credentials) *provider.Registry {
	registry := provider.NewRegistry()
	for name, providerCfg := range cfg.Providers {
		registry.RegisterFactory(name, provider.NewOllamaFactory(name, providerCfg.Endpoint))
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

type services struct {
	proxy        *mcp.Proxy
	lspManager   *lsp.Manager
	webCache     *store.Cache
	readHandler  *mcptools.ReadHandler
	editHandler  *mcptools.EditHandler
	shellHandler *mcptools.ShellHandler
	fileTracker  *mcptools.FileReadTracker
	deltaTracker *delta.Tracker
	scratchpad   *mcptools.Scratchpad
	shell        *shell.Shell
	exaKey       string
}

func setupServices(cfg *config.Config, creds *config.Credentials) services {
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

	webCache := openWebCache(cfg)

	// Create delta tracker for undo support, sharing the same DB.
	var dt *delta.Tracker
	if webCache != nil {
		dt = delta.New(webCache.DB())
	}

	editHandler := mcptools.NewEditHandler(fileTracker, lspManager, dt)
	proxy.RegisterTool(mcptools.NewEditTool(), editHandler.Handle)

	proxy.RegisterTool(mcptools.NewWebFetchTool(), mcptools.MakeWebFetchHandler(webCache))

	exaKey := creds.GetAPIKey("exa_ai")
	proxy.RegisterTool(mcptools.NewWebSearchTool(), mcptools.MakeWebSearchHandler(webCache, exaKey, ""))

	// Shell tool — in-process POSIX interpreter with command blocking.
	sh := shell.New("", shell.DefaultBlockFuncs())
	shellHandler := mcptools.NewShellHandler(sh)
	proxy.RegisterTool(mcptools.NewShellTool(), shellHandler.Handle)

	// TodoWrite tool — agent scratchpad for plan/notes recitation.
	pad := &mcptools.Scratchpad{}
	proxy.RegisterTool(mcptools.NewTodoWriteTool(), mcptools.MakeTodoWriteHandler(pad))

	return services{
		proxy:        proxy,
		lspManager:   lspManager,
		webCache:     webCache,
		readHandler:  readHandler,
		editHandler:  editHandler,
		shellHandler: shellHandler,
		fileTracker:  fileTracker,
		deltaTracker: dt,
		scratchpad:   pad,
		shell:        sh,
		exaKey:       exaKey,
	}
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
	if _, err := rand.Read(b); err != nil {
		log.Warn().Err(err).Msg("failed to read random bytes for session id")
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
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

func listSessions(db *store.Cache) {
	if db == nil {
		fmt.Println("No cache available")
		return
	}
	sessions, err := db.ListSessions()
	if err != nil {
		fmt.Printf("Error listing sessions: %v\n", err)
		return
	}
	if len(sessions) == 0 {
		fmt.Println("No sessions found")
		return
	}
	for _, s := range sessions {
		ts := s.Timestamp.Format("2006-01-02 15:04")
		preview := s.Preview
		preview = strings.ReplaceAll(preview, "\n", " ")
		if len(preview) > 50 {
			preview = preview[:50]
		}
		fmt.Printf("%s  %s  %s\n", s.ID, ts, preview)
	}
}

func storedToMessages(msgs []store.SessionMessage) []provider.Message {
	return store.ToProviderMessages(msgs)
}

func resolveSession(flagSession string, flagContinue bool, db *store.Cache) (string, []provider.Message) {
	switch {
	case flagSession != "":
		if db != nil {
			ok, err := db.SessionExists(flagSession)
			if err != nil || !ok {
				fmt.Printf("Session %q not found\n", flagSession)
				os.Exit(1)
			}
		}
		msgs := loadHistory(flagSession, db)
		return flagSession, msgs

	case flagContinue:
		if db == nil {
			fmt.Println("No cache available")
			os.Exit(1)
		}
		id, err := db.LatestSessionID()
		if err != nil {
			fmt.Printf("No sessions to continue: %v\n", err)
			os.Exit(1)
		}
		msgs := loadHistory(id, db)
		return id, msgs

	default:
		sid := newSessionID()
		if db != nil {
			if err := db.CreateSession(sid); err != nil {
				fmt.Printf("Warning: failed to create session: %v\n", err)
			}
		}
		return sid, nil
	}
}

func loadHistory(sessionID string, db *store.Cache) []provider.Message {
	if db == nil {
		return nil
	}
	stored, err := db.LoadMessages(sessionID)
	if err != nil {
		fmt.Printf("Warning: failed to load session history: %v\n", err)
		return nil
	}
	return storedToMessages(stored)
}
