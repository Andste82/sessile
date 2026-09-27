package mcp

import (
	"regexp"
	"strings"
)

// Whether a command is heavy enough to queue (PROJECT_PLAN.md §4.12.5).
//
// The agent says so: it wrote the command and knows whether it builds
// something. Sessile only fills in the obvious cases, for the agent that
// forgets — and leans towards "not heavy", because the cost of a wrong
// "heavy" is an agent waiting to read a file, while the cost of a wrong
// "light" is one extra build running.

// heavyCommands are the ones nobody needs to declare.
var heavyCommands = map[string]bool{
	"make": true, "ninja": true, "bazel": true, "gradle": true, "gradlew": true,
	"mvn": true, "msbuild": true, "meson": true, "scons": true, "tup": true,
	"pytest": true, "tox": true, "ctest": true, "just": true, "task": true,
}

// heavySubcommands are tools where only some subcommands are heavy.
var heavySubcommands = map[string][]string{
	"cargo":  {"build", "test", "bench", "clippy", "check", "install"},
	"go":     {"build", "test", "install", "vet", "generate"},
	"docker": {"build", "buildx", "compose"},
	"npm":    {"run", "test", "install", "ci", "rebuild"},
	"yarn":   {"build", "test", "install"},
	"pnpm":   {"build", "test", "install"},
	"dotnet": {"build", "test", "publish", "restore"},
	"cmake":  {"--build"},
	"swift":  {"build", "test"},
	"zig":    {"build", "test"},
}

// scriptRe is a script invocation — ./build.sh, bash ci.sh, python setup.py.
// Most of those an agent runs are builds in a coat.
var scriptRe = regexp.MustCompile(`^\./|\.(sh|bash|zsh|ps1|py|rb|js|mjs)$`)

// shellWrappers are prefixes to look past before judging a command.
var shellWrappers = map[string]bool{
	"sudo": true, "env": true, "nice": true, "ionice": true, "time": true,
	"stdbuf": true, "nohup": true, "exec": true,
}

// base is the last path element of a command word: /usr/bin/make → make.
func base(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isHeavy reports whether a command should queue. declared is the agent's
// own answer and wins; anything else falls back to the patterns.
func isHeavy(command, declared string) bool {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case "heavy":
		return true
	case "light":
		return false
	}
	for _, segment := range splitCommand(command) {
		if segmentIsHeavy(segment) {
			return true
		}
	}
	return false
}

// splitCommand breaks a command line into the pieces that each run
// something, so `git pull && make` is seen as a build.
func splitCommand(command string) []string {
	fields := regexp.MustCompile(`&&|\|\||;|\||\n`).Split(command, -1)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func segmentIsHeavy(segment string) bool {
	words := strings.Fields(segment)
	for len(words) > 0 {
		w := words[0]
		// Skip environment assignments and wrappers: `CC=clang nice make`.
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") {
			words = words[1:]
			continue
		}
		if shellWrappers[base(w)] {
			words = words[1:]
			continue
		}
		break
	}
	if len(words) == 0 {
		return false
	}
	name := base(words[0])
	if heavyCommands[name] {
		return true
	}
	if subs, ok := heavySubcommands[name]; ok {
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-") && name != "cmake" {
				continue
			}
			for _, sub := range subs {
				if w == sub {
					return true
				}
			}
			break
		}
		return false
	}
	// A script, by shape: ./build.sh, bash ci.sh, python setup.py.
	if scriptRe.MatchString(words[0]) {
		return true
	}
	if len(words) > 1 && (name == "bash" || name == "sh" || name == "zsh" ||
		name == "python" || name == "python3" || name == "node" || name == "pwsh" || name == "powershell") {
		return scriptRe.MatchString(words[1])
	}
	return false
}
