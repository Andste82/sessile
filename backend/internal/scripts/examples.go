package scripts

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// The built-in example extensions (§4.15.6): Jira, Jenkins and Artifactory,
// plain requests, embedded as folders and zipped on demand. Installing one
// goes through exactly the path an upload does; the installed copy is the
// user's, and a newer example in a later release is only offered, never
// applied.
//
//go:embed examples
var examplesFS embed.FS

// Example is a built-in extension as the Scripts page lists it.
type Example struct {
	Meta Meta
	// Installed is the version the user has installed under the example's
	// name, "" if none.
	Installed       string
	UpdateAvailable bool
}

// Examples lists the built-in extensions, with the user's installed versions.
func (s *Store) Examples(userID string) ([]Example, error) {
	entries, err := fs.ReadDir(examplesFS, "examples")
	if err != nil {
		return nil, err
	}
	var out []Example
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := examplesFS.ReadFile("examples/" + e.Name() + "/meta.json")
		if err != nil {
			return nil, err
		}
		m, err := ParseMeta(raw)
		if err != nil {
			return nil, fmt.Errorf("example %s: %w", e.Name(), err)
		}
		ex := Example{Meta: m}
		if inst, err := s.Get(userID, m.Name); err == nil {
			ex.Installed = inst.Version
			ex.UpdateAvailable = newer(m.Version, inst.Version)
		}
		out = append(out, ex)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.Name < out[j].Meta.Name })
	return out, nil
}

// ExampleZip zips a built-in extension.
func ExampleZip(name string) ([]byte, error) {
	if !ValidName(name) {
		return nil, ErrNotFound
	}
	root := "examples/" + name
	if _, err := fs.Stat(examplesFS, root+"/meta.json"); err != nil {
		return nil, ErrNotFound
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := fs.WalkDir(examplesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := examplesFS.ReadFile(p)
		if err != nil {
			return err
		}
		w, err := zw.Create(strings.TrimPrefix(p, root+"/"))
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// newer reports whether semver a is above b (pre-release tags ignored).
func newer(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	v, _, _ = strings.Cut(v, "-")
	v, _, _ = strings.Cut(v, "+")
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}
