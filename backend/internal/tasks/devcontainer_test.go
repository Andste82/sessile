package tasks

import (
	"strings"
	"testing"
)

// A devcontainer is used, never managed (§4.12.3): one `devcontainer up` with
// the repo as its workspace, and `devcontainer exec` for every command the
// agent runs in it. Sessile builds no images and cleans nothing up.
func TestDevcontainerUp(t *testing.T) {
	dir := "/home/ob/sessile-tasks/dbg-142-print-crash-3f9a1c"
	tests := []struct {
		name string
		dc   Devcontainer
		want []string
		not  []string
	}{
		{
			name: "repo config",
			dc:   Devcontainer{Mode: "repo"},
			want: []string{"devcontainer up", "--workspace-folder '" + dir + "/repo'"},
			not:  []string{"--config", "--mount"},
		},
		{
			name: "generic config",
			dc:   Devcontainer{Mode: "generic"},
			want: []string{"--config '" + dir + "/.devcontainer-generic/devcontainer.json'"},
		},
		{
			name: "docker socket is a per-task opt-in",
			dc:   Devcontainer{Mode: "auto", DockerSocket: true},
			want: []string{"--mount 'type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock'"},
		},
		{
			name: "auto uses the repo's own config",
			dc:   Devcontainer{Mode: "auto"},
			not:  []string{"--config"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := devcontainerUp(&tt.dc, dir)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tt.not {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}
