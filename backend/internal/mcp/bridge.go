package mcp

import (
	"embed"
)

// The cross-compiled cmd/sessile-mcp binaries (§4.17.3), built into
// bridge/ by `make bridge` before the server is built.
//
//go:embed bridge
var bridgeFS embed.FS

// Bridge returns the bridge binary for a target, and false when this build
// has none for it — tasks then run without sessile's tools.
func Bridge(goos, goarch string) ([]byte, bool) {
	name := "bridge/sessile-mcp-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	b, err := bridgeFS.ReadFile(name)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Bridge is tasks.ToolServer's access to the embedded binaries.
func (s *Server) Bridge(goos, goarch string) ([]byte, bool) { return Bridge(goos, goarch) }
