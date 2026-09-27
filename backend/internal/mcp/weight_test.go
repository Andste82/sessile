package mcp

import "testing"

// What queues is the agent's call, and sessile only fills in the obvious
// cases (§4.12.5). A wrong "heavy" costs an agent a wait; a wrong "light"
// costs one extra build — so the defaults lean light.
func TestIsHeavy(t *testing.T) {
	tests := []struct {
		command  string
		declared string
		want     bool
	}{
		// The agent's own answer wins, whatever the command looks like.
		{"ls -la", "heavy", true},
		{"make -j64", "light", false},

		// Builds and suites nobody should have to declare.
		{"make", "", true},
		{"cd repo && make test", "", true},
		{"ninja -C build", "", true},
		{"cargo build --release", "", true},
		{"go test ./...", "", true},
		{"docker build -t x .", "", true},
		{"npm run build", "", true},
		{"cmake --build build", "", true},
		{"CC=clang nice make all", "", true},
		{"pytest -q", "", true},

		// Scripts are builds in a coat more often than not.
		{"./build.sh", "", true},
		{"bash ci.sh --release", "", true},
		{"python setup.py bdist_wheel", "", true},

		// The ordinary traffic of a task, which must never wait.
		{"ls -la", "", false},
		{"git status", "", false},
		{"grep -rn TODO src", "", false},
		{"cat README.md", "", false},
		{"go version", "", false},
		{"npm --version", "", false},
		{"docker ps", "", false},
		{"cargo --help", "", false},
		{"python -c 'print(1)'", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got := isHeavy(tt.command, tt.declared); got != tt.want {
			t.Errorf("isHeavy(%q, %q) = %v, want %v", tt.command, tt.declared, got, tt.want)
		}
	}
}
