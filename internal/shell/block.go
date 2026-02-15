// Package shell provides an in-process POSIX shell interpreter with command
// blocking for safe LLM-driven execution.
package shell

import "strings"

// BlockFunc returns true if the given command args should be blocked.
type BlockFunc func(args []string) bool

// CommandsBlocker returns a BlockFunc that blocks exact command name matches.
func CommandsBlocker(cmds []string) BlockFunc {
	blocked := make(map[string]struct{}, len(cmds))
	for _, c := range cmds {
		blocked[c] = struct{}{}
	}
	return func(args []string) bool {
		if len(args) == 0 {
			return false
		}
		_, ok := blocked[args[0]]
		return ok
	}
}

// ArgumentsBlocker returns a BlockFunc that blocks a command when specific
// subcommand args and/or flags are present.
//
// For example, ArgumentsBlocker("npm", []string{"install"}, []string{"-g"})
// blocks "npm install -g <pkg>" but allows "npm install <pkg>".
func ArgumentsBlocker(cmd string, subArgs, flags []string) BlockFunc {
	return func(args []string) bool {
		if len(args) == 0 || args[0] != cmd {
			return false
		}
		posArgs, posFlags := splitArgsFlags(args[1:])
		if !prefixMatch(posArgs, subArgs) {
			return false
		}
		if len(flags) > 0 && !flagsPresent(posFlags, flags) {
			return false
		}
		return true
	}
}

// splitArgsFlags separates positional arguments from flags (anything
// starting with '-').
func splitArgsFlags(args []string) (positional, flags []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return
}

// prefixMatch returns true if haystack starts with all elements of needle.
func prefixMatch(haystack, needle []string) bool {
	if len(haystack) < len(needle) {
		return false
	}
	for i, n := range needle {
		if haystack[i] != n {
			return false
		}
	}
	return true
}

// flagsPresent returns true if all required flags appear in the actual flags.
func flagsPresent(actual, required []string) bool {
	have := make(map[string]struct{}, len(actual))
	for _, f := range actual {
		have[f] = struct{}{}
	}
	for _, r := range required {
		if _, ok := have[r]; !ok {
			return false
		}
	}
	return true
}

// BannedCommands is the default set of commands blocked for security.
var BannedCommands = []string{
	// Bypass vectors — block shells, interpreters, and indirection commands
	// that could re-exec blocked commands or run arbitrary network code.
	"bash", "sh", "zsh", "fish", "csh", "tcsh", "ksh", "dash",
	"env", "nohup", "xargs", "strace", "ltrace",
	"python", "python3", "python2", "node", "ruby", "perl",
	"php", "lua", "tclsh", "wish",
	// Network / download
	"aria2c", "axel", "curl", "curlie", "http-prompt", "httpie",
	"links", "lynx", "nc", "ncat", "scp", "sftp", "ssh",
	"telnet", "w3m", "wget", "xh",
	// Privilege escalation
	"doas", "su", "sudo",
	// Package managers
	"apk", "apt", "apt-cache", "apt-get", "dnf", "dpkg", "emerge",
	"home-manager", "makepkg", "opkg", "pacman", "paru", "pkg",
	"pkg_add", "pkg_delete", "portage", "rpm", "yay", "yum", "zypper",
	// System modification
	"at", "batch", "chkconfig", "crontab", "fdisk", "mkfs", "mount",
	"parted", "service", "systemctl", "umount",
	// Network configuration
	"firewall-cmd", "ifconfig", "ip", "iptables", "netstat", "pfctl",
	"route", "ufw",
	// Directory traversal is handled by cwd clamping in updateFromRunner,
	// not by command blocking (cd is a shell builtin, invisible to ExecHandlers).
}

// DefaultBlockFuncs returns the standard set of block functions.
func DefaultBlockFuncs() []BlockFunc {
	return []BlockFunc{
		CommandsBlocker(BannedCommands),
		// Block global package installs
		ArgumentsBlocker("npm", []string{"install"}, []string{"-g"}),
		ArgumentsBlocker("npm", []string{"install"}, []string{"--global"}),
		ArgumentsBlocker("pnpm", []string{"add"}, []string{"-g"}),
		ArgumentsBlocker("pnpm", []string{"add"}, []string{"--global"}),
		ArgumentsBlocker("yarn", []string{"global"}, nil),
		ArgumentsBlocker("pip", []string{"install"}, nil),
		ArgumentsBlocker("pip3", []string{"install"}, nil),
		ArgumentsBlocker("gem", []string{"install"}, nil),
		ArgumentsBlocker("cargo", []string{"install"}, nil),
		ArgumentsBlocker("go", []string{"install"}, nil),
		// Block go test -exec (code execution escape)
		ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),
	}
}
