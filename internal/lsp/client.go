// Package lsp wraps powernap to provide LSP diagnostics for file mutations.
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/rs/zerolog/log"
)

// Severity constants matching LSP DiagnosticSeverity.
const (
	SeverityError   = 1
	SeverityWarning = 2
)

// Client wraps a powernap LSP client with diagnostics tracking.
type Client struct {
	inner    *powernap.Client
	serverID string

	mu          sync.Mutex
	diags       map[string][]protocol.Diagnostic // uri -> diagnostics
	versions    map[string]int                   // uri -> document version
	diagChanged chan struct{}                    // signaled on publishDiagnostics
}

// newClient spawns an LSP server and returns a wrapped client.
func newClient(serverID string, cfg powernap.ClientConfig) (*Client, error) {
	inner, err := powernap.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", serverID, err)
	}

	c := &Client{
		inner:       inner,
		serverID:    serverID,
		diags:       make(map[string][]protocol.Diagnostic),
		versions:    make(map[string]int),
		diagChanged: make(chan struct{}, 1),
	}

	// Register publishDiagnostics handler before Initialize.
	inner.RegisterNotificationHandler(
		"textDocument/publishDiagnostics",
		func(_ context.Context, _ string, params json.RawMessage) {
			var p protocol.PublishDiagnosticsParams
			if err := json.Unmarshal(params, &p); err != nil {
				log.Error().Err(err).Msg("lsp: unmarshal diagnostics")
				return
			}
			c.mu.Lock()
			c.diags[string(p.URI)] = p.Diagnostics
			c.mu.Unlock()

			// Non-blocking signal.
			select {
			case c.diagChanged <- struct{}{}:
			default:
			}
		},
	)

	// Stub handlers so the server doesn't error on common requests.
	inner.RegisterHandler("window/workDoneProgress/create",
		func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return nil, nil
		},
	)
	inner.RegisterNotificationHandler("$/progress",
		func(_ context.Context, _ string, _ json.RawMessage) {},
	)
	inner.RegisterNotificationHandler("window/logMessage",
		func(_ context.Context, _ string, _ json.RawMessage) {},
	)
	inner.RegisterHandler("client/registerCapability",
		func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return nil, nil
		},
	)

	return c, nil
}

// initialize sends initialize+initialized to the server.
func (c *Client) initialize(ctx context.Context) error {
	return c.inner.Initialize(ctx, false)
}

// openFile reads a file from disk and sends textDocument/didOpen.
// If already open, sends didChange instead.
func (c *Client) openFile(ctx context.Context, absPath string) error {
	uri := string(protocol.URIFromPath(absPath))

	c.mu.Lock()
	_, alreadyOpen := c.versions[uri]
	c.mu.Unlock()
	if alreadyOpen {
		return c.notifyChange(ctx, absPath)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("lsp: read %s: %w", absPath, err)
	}

	lang := powernap.DetectLanguage(absPath)

	c.mu.Lock()
	c.versions[uri] = 0
	c.mu.Unlock()

	return c.inner.NotifyDidOpenTextDocument(ctx, uri, string(lang), 0, string(data))
}

// notifyChange reads a file from disk and sends textDocument/didChange.
func (c *Client) notifyChange(ctx context.Context, absPath string) error {
	uri := string(protocol.URIFromPath(absPath))

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("lsp: read %s: %w", absPath, err)
	}

	c.mu.Lock()
	v := c.versions[uri] + 1
	c.versions[uri] = v
	c.mu.Unlock()

	change := protocol.TextDocumentContentChangeEvent{
		Value: protocol.TextDocumentContentChangeWholeDocument{
			Text: string(data),
		},
	}
	return c.inner.NotifyDidChangeTextDocument(ctx, uri, v, []protocol.TextDocumentContentChangeEvent{change})
}

// waitForDiagnostics blocks until diagnostics arrive or timeout expires.
func (c *Client) waitForDiagnostics(ctx context.Context, absPath string, timeout time.Duration) []protocol.Diagnostic {
	uri := string(protocol.URIFromPath(absPath))
	deadline := time.After(timeout)

	const debounce = 150 * time.Millisecond
	var timer *time.Timer

	for {
		select {
		case <-c.diagChanged:
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(debounce)
		case <-timerChan(timer):
			c.mu.Lock()
			d := c.diags[uri]
			c.mu.Unlock()
			return d
		case <-deadline:
			c.mu.Lock()
			d := c.diags[uri]
			c.mu.Unlock()
			return d
		case <-ctx.Done():
			c.mu.Lock()
			d := c.diags[uri]
			c.mu.Unlock()
			return d
		}
	}
}

// close gracefully shuts down the LSP server.
func (c *Client) close(ctx context.Context) error {
	if err := c.inner.Shutdown(ctx); err != nil {
		c.inner.Kill()
		return fmt.Errorf("lsp: shutdown %s: %w", c.serverID, err)
	}
	return c.inner.Exit()
}

// drainDiagChan drains stale signals before waiting.
func (c *Client) drainDiagChan() {
	for {
		select {
		case <-c.diagChanged:
		default:
			return
		}
	}
}

// notifyAndWait opens (or re-notifies) the file and waits for diagnostics.
// Uses openFile which handles both didOpen (first time) and didChange (subsequent).
func (c *Client) notifyAndWait(ctx context.Context, absPath string, timeout time.Duration) ([]protocol.Diagnostic, error) {
	c.drainDiagChan()
	if err := c.openFile(ctx, absPath); err != nil {
		return nil, err
	}
	return c.waitForDiagnostics(ctx, absPath, timeout), nil
}

// timerChan returns the timer's channel, or a nil channel if timer is nil.
func timerChan(t *time.Timer) <-chan time.Time {
	if t != nil {
		return t.C
	}
	return nil
}
