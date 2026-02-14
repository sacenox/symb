package lsp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/rs/zerolog/log"
)

// skipAutoStart lists generic commands that should not be auto-started.
// These interpreters/runners may trigger package downloads or run wrong binaries.
var skipAutoStart = map[string]bool{
	"npx":     true,
	"node":    true,
	"python":  true,
	"python3": true,
	"java":    true,
	"ruby":    true,
	"perl":    true,
	"dotnet":  true,
	"bun":     true,
}

// DiagCallback is called when diagnostics change for a file.
// absPath is the filesystem path; lines maps 0-indexed line numbers to max severity.
type DiagCallback func(absPath string, lines map[int]int)

// Manager manages LSP server lifecycles keyed by server name.
type Manager struct {
	cfgMgr *powernapconfig.Manager

	mu      sync.Mutex
	clients map[string]*Client // serverName -> client
	broken  map[string]bool    // servers that failed to start

	callback DiagCallback
}

// NewManager creates a manager with powernap's built-in server defaults.
func NewManager() *Manager {
	// Silence powernap's slog output — it writes to stderr which the TUI owns.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	cm := powernapconfig.NewManager()
	_ = cm.LoadDefaults()
	return &Manager{
		cfgMgr:  cm,
		clients: make(map[string]*Client),
		broken:  make(map[string]bool),
	}
}

// SetCallback sets the function called when diagnostics change.
func (m *Manager) SetCallback(cb DiagCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callback = cb
}

// TouchFile ensures the right LSP servers are running for this file and
// sends didOpen/didChange. Non-blocking — errors are logged, not returned.
func (m *Manager) TouchFile(ctx context.Context, absPath string) {
	clients := m.ensureClients(ctx, absPath)
	for _, c := range clients {
		if err := c.openFile(ctx, absPath); err != nil {
			log.Error().Err(err).Str("server", c.serverID).Msg("lsp: touchFile openFile")
		}
	}
}

// NotifyAndWait notifies relevant LSP servers of a file change and waits for
// diagnostics. Returns aggregated diagnostics across all matching servers.
func (m *Manager) NotifyAndWait(ctx context.Context, absPath string, timeout time.Duration) []protocol.Diagnostic {
	clients := m.ensureClients(ctx, absPath)
	if len(clients) == 0 {
		return nil
	}

	var all []protocol.Diagnostic
	for _, c := range clients {
		diags, err := c.notifyAndWait(ctx, absPath, timeout)
		if err != nil {
			log.Error().Err(err).Str("server", c.serverID).Msg("lsp: notifyAndWait")
			continue
		}
		log.Debug().Str("server", c.serverID).Int("count", len(diags)).Str("file", absPath).Msg("lsp: diagnostics received")
		all = append(all, diags...)
	}

	log.Debug().Int("total", len(all)).Str("file", absPath).Msg("lsp: aggregated diagnostics")

	// Fire callback once with aggregated diagnostics from all servers.
	m.mu.Lock()
	cb := m.callback
	m.mu.Unlock()
	if cb != nil {
		cb(absPath, diagLineSeverities(all))
	}

	return all
}

// StopAll gracefully shuts down all running LSP servers.
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.Unlock()

	for _, c := range clients {
		if err := c.close(ctx); err != nil {
			log.Error().Err(err).Str("server", c.serverID).Msg("lsp: stopAll")
		}
	}
}

// serverToStart holds info needed to start an LSP server outside the lock.
type serverToStart struct {
	name    string
	cfg     *powernapconfig.ServerConfig
	root    string
	cmdPath string
}

// ensureClients finds or starts LSP servers for the given file.
func (m *Manager) ensureClients(ctx context.Context, absPath string) []*Client {
	lang := string(powernap.DetectLanguage(absPath))
	if lang == "" {
		log.Debug().Str("file", absPath).Msg("lsp: unknown language, skipping")
		return nil
	}

	log.Debug().Str("file", absPath).Str("lang", lang).Msg("lsp: ensureClients")

	servers := m.cfgMgr.GetServers()

	// Phase 1: under lock, collect existing clients and identify servers to start.
	m.mu.Lock()
	var result []*Client
	var pending []serverToStart

	for name, cfg := range servers {
		if !matchesFileType(cfg, lang) {
			continue
		}
		if m.broken[name] {
			continue
		}
		if c, ok := m.clients[name]; ok {
			result = append(result, c)
			continue
		}
		if skipAutoStart[cfg.Command] {
			m.broken[name] = true
			continue
		}
		cmdPath := lookPath(cfg.Command)
		if cmdPath == "" {
			m.broken[name] = true
			continue
		}
		root := findRoot(absPath, cfg.RootMarkers)
		if root == "" {
			root, _ = os.Getwd()
		}
		pending = append(pending, serverToStart{name: name, cfg: cfg, root: root, cmdPath: cmdPath})
	}
	m.mu.Unlock()

	// Phase 2: start servers without holding the lock (blocking I/O).
	for _, s := range pending {
		c, err := m.startClient(ctx, s.name, s.cfg, s.root, s.cmdPath)

		m.mu.Lock()
		if err != nil {
			log.Error().Err(err).Str("server", s.name).Msg("lsp: start failed")
			m.broken[s.name] = true
		} else {
			m.clients[s.name] = c
			result = append(result, c)
		}
		m.mu.Unlock()
	}

	return result
}

// startClient spawns and initializes a single LSP server. Must be called with m.mu held.
func (m *Manager) startClient(ctx context.Context, name string, cfg *powernapconfig.ServerConfig, root, cmdPath string) (*Client, error) {
	rootURI := string(protocol.URIFromPath(root))

	pcfg := powernap.ClientConfig{
		Command:     cmdPath,
		Args:        cfg.Args,
		RootURI:     rootURI,
		Environment: cfg.Environment,
		Settings:    cfg.Settings,
		InitOptions: cfg.InitOptions,
		WorkspaceFolders: []protocol.WorkspaceFolder{
			{URI: rootURI, Name: filepath.Base(root)},
		},
	}

	c, err := newClient(name, pcfg)
	if err != nil {
		return nil, err
	}

	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := c.initialize(initCtx); err != nil {
		_ = c.close(ctx)
		return nil, fmt.Errorf("initialize: %w", err)
	}

	log.Info().Str("server", name).Str("root", root).Str("cmd", cmdPath).Msg("lsp: server started")
	return c, nil
}

// matchesFileType checks if a server config handles the given language ID.
func matchesFileType(cfg *powernapconfig.ServerConfig, lang string) bool {
	for _, ft := range cfg.FileTypes {
		if ft == lang {
			return true
		}
	}
	return false
}

// findRoot walks up from the file looking for any of the root markers.
func findRoot(absPath string, markers []string) string {
	dir := filepath.Dir(absPath)
	for {
		for _, marker := range markers {
			matches, _ := filepath.Glob(filepath.Join(dir, marker))
			if len(matches) > 0 {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// diagLineSeverities converts a diagnostic slice to a line->severity map.
// Only tracks errors (1) and warnings (2). Lower severity number wins.
func diagLineSeverities(diags []protocol.Diagnostic) map[int]int {
	if len(diags) == 0 {
		return nil
	}
	lines := make(map[int]int)
	for _, d := range diags {
		sev := int(d.Severity)
		if sev != SeverityError && sev != SeverityWarning {
			continue
		}
		line := int(d.Range.Start.Line) // 0-indexed
		if existing, ok := lines[line]; !ok || sev < existing {
			lines[line] = sev
		}
	}
	return lines
}

// FormatDiagnostics formats diagnostics as a text block for LLM tool responses.
// Returns empty string if no errors or warnings.
func FormatDiagnostics(displayPath string, diags []protocol.Diagnostic) string {
	if len(diags) == 0 {
		return ""
	}

	var buf []byte
	count := 0
	for _, d := range diags {
		sev := int(d.Severity)
		if sev != SeverityError && sev != SeverityWarning {
			continue
		}
		if count == 0 {
			buf = append(buf, fmt.Sprintf("\nLSP diagnostics:\n<diagnostics file=%q>\n", displayPath)...)
		}
		label := "WARNING"
		if sev == SeverityError {
			label = "ERROR"
		}
		buf = append(buf, fmt.Sprintf("%s [%d:%d] %s\n",
			label,
			d.Range.Start.Line+1, // display as 1-indexed
			d.Range.Start.Character+1,
			d.Message,
		)...)
		count++
		if count >= 20 {
			remaining := 0
			for _, dd := range diags {
				if int(dd.Severity) == SeverityError || int(dd.Severity) == SeverityWarning {
					remaining++
				}
			}
			remaining -= count
			if remaining > 0 {
				buf = append(buf, fmt.Sprintf("... and %d more\n", remaining)...)
			}
			break
		}
	}
	if count == 0 {
		return ""
	}
	buf = append(buf, "</diagnostics>"...)
	return string(buf)
}

// lookPath finds a command binary, checking PATH first, then common
// language-specific bin directories that may not be in PATH.
func lookPath(command string) string {
	if p, err := exec.LookPath(command); err == nil {
		return p
	}

	// Extra directories where language toolchains install binaries.
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	var extras []string

	// Go: $GOBIN or $GOPATH/bin or ~/go/bin
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		extras = append(extras, gobin)
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		extras = append(extras, filepath.Join(gopath, "bin"))
	}
	extras = append(extras, filepath.Join(home, "go", "bin"))

	// Rust: ~/.cargo/bin
	extras = append(extras, filepath.Join(home, ".cargo", "bin"))

	// Local bin
	extras = append(extras, filepath.Join(home, ".local", "bin"))

	for _, dir := range extras {
		p := filepath.Join(dir, command)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
