package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Shell provides an in-process POSIX shell with persistent cwd/env across calls.
type Shell struct {
	mu         sync.Mutex
	root       string // project root — shell is anchored here
	cwd        string
	env        []string
	blockFuncs []BlockFunc
}

// New creates a Shell rooted at cwd with the given block functions.
// The shell is anchored to this directory — cd outside it is clamped back.
func New(cwd string, blockers []BlockFunc) *Shell {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return &Shell{
		root:       cwd,
		cwd:        cwd,
		env:        os.Environ(),
		blockFuncs: blockers,
	}
}

// Exec runs a command synchronously, returning stdout, stderr, and any error.
func (s *Shell) Exec(ctx context.Context, command string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var stdout, stderr bytes.Buffer
	err := s.execCommon(ctx, command, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// ExecStream runs a command, streaming output to the provided writers.
func (s *Shell) ExecStream(ctx context.Context, command string, stdout, stderr io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.execCommon(ctx, command, stdout, stderr)
}

// Dir returns the current working directory.
func (s *Shell) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *Shell) execCommon(ctx context.Context, command string, stdout, stderr io.Writer) (err error) {
	var runner *interp.Runner
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("command execution panic: %v", r)
		}
		if runner != nil {
			s.updateFromRunner(runner, stderr)
		}
	}()

	parsed, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("could not parse command: %w", err)
	}

	runner, err = s.newInterp(stdout, stderr)
	if err != nil {
		return fmt.Errorf("could not create interpreter: %w", err)
	}

	return runner.Run(ctx, parsed)
}

func (s *Shell) newInterp(stdout, stderr io.Writer) (*interp.Runner, error) {
	return interp.New(
		interp.StdIO(nil, stdout, stderr),
		interp.Interactive(false),
		interp.Env(expand.ListEnviron(s.env...)),
		interp.Dir(s.cwd),
		interp.ExecHandlers(s.blockHandler()),
	)
}

func (s *Shell) blockHandler() func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			for _, bf := range s.blockFuncs {
				if bf(args) {
					return fmt.Errorf("command blocked: %q", args[0])
				}
			}
			return next(ctx, args)
		}
	}
}

// updateFromRunner persists cwd and exported env vars after execution.
// If the runner's cwd escaped the project root, it is clamped back and a
// warning is written to stderr so the LLM knows.
func (s *Shell) updateFromRunner(runner *interp.Runner, stderr io.Writer) {
	dir := runner.Dir
	if !isSubdir(dir, s.root) {
		fmt.Fprintf(stderr, "[cd rejected: you are anchored to %s]\n", s.root)
		dir = s.root
	}
	s.cwd = dir
	s.env = s.env[:0]
	runner.Env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported {
			s.env = append(s.env, name+"="+vr.Str)
		}
		return true
	})
}

// isSubdir reports whether dir is equal to or under root.
func isSubdir(dir, root string) bool {
	return dir == root || strings.HasPrefix(dir, root+string(os.PathSeparator))
}

// ExitCode extracts the exit code from an interpreter error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr interp.ExitStatus
	if errors.As(err, &exitErr) {
		return int(exitErr)
	}
	return 1
}
