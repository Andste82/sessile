package scripts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const echoMeta = `{
  "name": "echo",
  "version": "1.0.0",
  "description": "test script",
  "runtime": "python3",
  "settings": [
    {"name": "ECHO_URL", "label": "URL", "type": "url", "required": true, "context": true},
    {"name": "ECHO_TOKEN", "label": "Token", "type": "secret", "required": true}
  ],
  "check": "whoami",
  "timeoutSeconds": 3,
  "functions": [
    {"name": "whoami", "effect": "read", "description": "who", "input": {"type": "object"}},
    {"name": "echo", "effect": "read", "description": "echo input",
     "input": {"type": "object", "required": ["key"], "properties": {"key": {"type": "string", "pattern": "^[A-Z]+-[0-9]+$"}}}},
    {"name": "leak", "effect": "read", "description": "prints its token"},
    {"name": "sleep", "effect": "write", "description": "sleeps"},
    {"name": "fail", "effect": "read", "description": "fails"},
    {"name": "garbage", "effect": "read", "description": "not json"}
  ]
}`

const echoMain = `import json, os, sys, time
fn = sys.argv[1]
data = json.load(sys.stdin)
if fn == "whoami":
    print(json.dumps({"url": os.environ["ECHO_URL"], "home": os.environ.get("HOME"), "path": os.environ.get("PATH")}))
elif fn == "echo":
    print(json.dumps({"got": data, "extra_env": sorted(k for k in os.environ if k.startswith("OTHER"))}))
elif fn == "leak":
    print(json.dumps({"auth": "Bearer " + os.environ["ECHO_TOKEN"]}))
    print("token was " + os.environ["ECHO_TOKEN"], file=sys.stderr)
elif fn == "sleep":
    time.sleep(30)
elif fn == "fail":
    print("boom", file=sys.stderr)
    sys.exit(2)
elif fn == "garbage":
    print("hello")
`

func makeZip(t *testing.T, files map[string]string, symlink string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if symlink != "" {
		h := &zip.FileHeader{Name: symlink}
		h.SetMode(os.ModeSymlink | 0o777)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte("/etc/passwd"))
	}
	zw.Close()
	return buf.Bytes()
}

func TestMetaValidation(t *testing.T) {
	bad := map[string]string{
		"secret as context":  `{"name":"x","version":"1.0.0","settings":[{"name":"T","label":"t","type":"secret","context":true}],"functions":[{"name":"f","effect":"read","description":"d"}]}`,
		"bad name":           `{"name":"X","version":"1.0.0","functions":[{"name":"f","effect":"read","description":"d"}]}`,
		"bad version":        `{"name":"x","version":"1","functions":[{"name":"f","effect":"read","description":"d"}]}`,
		"reserved setting":   `{"name":"x","version":"1.0.0","settings":[{"name":"PATH","label":"p","type":"string"}],"functions":[{"name":"f","effect":"read","description":"d"}]}`,
		"write check":        `{"name":"x","version":"1.0.0","check":"f","functions":[{"name":"f","effect":"write","description":"d"}]}`,
		"no functions":       `{"name":"x","version":"1.0.0","functions":[]}`,
		"unknown field":      `{"name":"x","version":"1.0.0","functions":[{"name":"f","effect":"read","description":"d"}],"surprise":1}`,
		"non-object schema":  `{"name":"x","version":"1.0.0","functions":[{"name":"f","effect":"read","description":"d","input":{"type":"string"}}]}`,
		"bad pattern":        `{"name":"x","version":"1.0.0","functions":[{"name":"f","effect":"read","description":"d","input":{"type":"object","properties":{"a":{"type":"string","pattern":"("}}}}]}`,
		"entry outside":      `{"name":"x","version":"1.0.0","entry":"../x.py","functions":[{"name":"f","effect":"read","description":"d"}]}`,
		"python2":            `{"name":"x","version":"1.0.0","runtime":"python2","functions":[{"name":"f","effect":"read","description":"d"}]}`,
		"choice w/o options": `{"name":"x","version":"1.0.0","settings":[{"name":"C","label":"c","type":"choice"}],"functions":[{"name":"f","effect":"read","description":"d"}]}`,
	}
	for name, raw := range bad {
		if _, err := ParseMeta([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseMeta([]byte(echoMeta)); err != nil {
		t.Fatalf("echo meta: %v", err)
	}
}

func TestSchemaValidation(t *testing.T) {
	m, _ := ParseMeta([]byte(echoMeta))
	f, _ := m.Function("echo")
	for input, ok := range map[string]bool{
		`{"key":"DBG-142"}`:            true,
		`{"key":"dbg-142"}`:            false,
		`{}`:                           false,
		`{"key":7}`:                    false,
		`[]`:                           false,
		`not json`:                     false,
		`{"key":"DBG-1","other":true}`: true,
	} {
		if err := ValidateInput(f, json.RawMessage(input)); (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", input, err, ok)
		}
	}
}

func TestInstallRejectsDangerousZips(t *testing.T) {
	s := NewStore(t.TempDir())
	good := map[string]string{"meta.json": echoMeta, "main.py": echoMain}
	cases := map[string][]byte{
		"zip slip":     makeZip(t, map[string]string{"meta.json": echoMeta, "main.py": echoMain, "../evil.py": "x"}, ""),
		"absolute":     makeZip(t, map[string]string{"meta.json": echoMeta, "main.py": echoMain, "/etc/evil": "x"}, ""),
		"backslash":    makeZip(t, map[string]string{"meta.json": echoMeta, "main.py": echoMain, `a\..\b`: "x"}, ""),
		"symlink":      makeZip(t, good, "link"),
		"no meta":      makeZip(t, map[string]string{"main.py": echoMain}, ""),
		"no entry":     makeZip(t, map[string]string{"meta.json": echoMeta}, ""),
		"not a zip":    []byte("hello"),
		"bomb":         makeZip(t, map[string]string{"meta.json": echoMeta, "main.py": echoMain, "big": strings.Repeat("a", maxUncompressed+1)}, ""),
		"too many":     manyEntries(t),
		"broken meta":  makeZip(t, map[string]string{"meta.json": "{", "main.py": echoMain}, ""),
		"traversal as": nil,
	}
	for name, data := range cases {
		if data == nil {
			continue
		}
		if _, err := s.Install("u1", data, "", false); err == nil {
			t.Errorf("%s: installed", name)
		}
	}
	if _, err := s.Install("u1", makeZip(t, good, ""), "../x", false); err == nil {
		t.Error("install as ../x accepted")
	}
	entries, _ := os.ReadDir(s.scriptsDir("u1"))
	if len(entries) != 0 {
		t.Errorf("a refused install left files behind: %v", entries)
	}
}

func manyEntries(t *testing.T) []byte {
	files := map[string]string{"meta.json": echoMeta, "main.py": echoMain}
	for i := 0; i < maxEntries; i++ {
		files["f"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26)] = "x"
	}
	return makeZip(t, files, "")
}

func TestInstallUpdateKeepsSettings(t *testing.T) {
	s := NewStore(t.TempDir())
	nested := map[string]string{"echo-1.0.0/meta.json": echoMeta, "echo-1.0.0/main.py": echoMain}
	m, err := s.Install("u1", makeZip(t, nested, ""), "", false)
	if err != nil || m.Name != "echo" {
		t.Fatalf("install nested: %+v %v", m, err)
	}
	if err := s.PutSettings("u1", "echo", Settings{Values: map[string]string{"ECHO_URL": "u", "ECHO_TOKEN": "t", "GONE": "x"}}); err != nil {
		t.Fatal(err)
	}
	var exists *ExistsError
	if _, err := s.Install("u1", makeZip(t, nested, ""), "", false); !errors.As(err, &exists) || exists.Installed != "1.0.0" {
		t.Fatalf("second install: %v", err)
	}
	v2 := strings.Replace(echoMeta, `"1.0.0"`, `"1.1.0"`, 1)
	if _, err := s.Install("u1", makeZip(t, map[string]string{"meta.json": v2, "main.py": echoMain}, ""), "", true); err != nil {
		t.Fatalf("update: %v", err)
	}
	st, _ := s.GetSettings("u1", "echo")
	if st.Values["ECHO_TOKEN"] != "t" || st.Values["GONE"] != "" {
		t.Fatalf("settings after update = %+v", st)
	}
	if m, _ := s.Get("u1", "echo"); m.Version != "1.1.0" {
		t.Fatalf("version = %s", m.Version)
	}
	// "Install as" a second instance.
	if m, err := s.Install("u1", makeZip(t, nested, ""), "echo-oss", false); err != nil || m.Name != "echo-oss" {
		t.Fatalf("install as: %+v %v", m, err)
	}
	if m, err := s.Get("u1", "echo-oss"); err != nil || m.Name != "echo-oss" {
		t.Fatalf("get echo-oss: %+v %v", m, err)
	}
	// The export has the code and never the settings.
	var buf bytes.Buffer
	if err := s.Export("u1", "echo", &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("ECHO_TOKEN: t")) {
		t.Fatal("export contains settings")
	}
	if _, err := s.Get("u2", "echo"); err != ErrNotFound {
		t.Fatalf("u2 sees u1's script: %v", err)
	}
}

func TestRunner(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	if err := exec.Command("python3", "-c", "import venv, ensurepip").Run(); err != nil {
		t.Skip("python3 without venv")
	}
	data := t.TempDir()
	s := NewStore(data)
	m, err := s.Install("u1", makeZip(t, map[string]string{"meta.json": echoMeta, "main.py": echoMain}, ""), "", false)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir("u1", "echo")
	r := NewRunner(data, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Setenv("OTHER_SECRET", "must-not-leak")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	ctx := context.Background()

	if _, err := r.Run(ctx, "u1", m, dir, Settings{}, "whoami", nil); err == nil || !strings.Contains(err.Error(), "needs setup") {
		t.Fatalf("without settings: %v", err)
	}
	st := Settings{Values: map[string]string{"ECHO_URL": "https://echo.example", "ECHO_TOKEN": "tok-SECRET-123"}}

	c := r.Check(ctx, "u1", m, dir, st)
	if !c.OK || !strings.Contains(c.Message, "https://echo.example") {
		t.Fatalf("check = %+v", c)
	}
	if st, _ := r.VenvStatus(m, dir); st != VenvReady {
		t.Fatalf("venv status = %s", st)
	}

	res, err := r.Run(ctx, "u1", m, dir, st, "echo", json.RawMessage(`{"key":"DBG-142"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), `"DBG-142"`) || strings.Contains(string(res.Output), "OTHER_SECRET") {
		t.Fatalf("echo output = %s", res.Output)
	}
	if _, err := r.Run(ctx, "u1", m, dir, st, "echo", json.RawMessage(`{"key":"nope"}`)); err == nil {
		t.Fatal("schema violation ran")
	}

	res, err = r.Run(ctx, "u1", m, dir, st, "leak", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Output)+res.Stderr, "tok-SECRET-123") || !strings.Contains(string(res.Output), "«ECHO_TOKEN»") {
		t.Fatalf("not redacted: %s / %s", res.Output, res.Stderr)
	}

	start := time.Now()
	if _, err := r.Run(ctx, "u1", m, dir, st, "sleep", nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("sleep: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout didn't kill the script")
	}
	if _, err := r.Run(ctx, "u1", m, dir, st, "fail", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("fail: %v", err)
	}
	if _, err := r.Run(ctx, "u1", m, dir, st, "garbage", nil); err == nil || !strings.Contains(err.Error(), "isn't JSON") {
		t.Fatalf("garbage: %v", err)
	}

	// Same requirements, same venv: a second script shares it.
	m2, _ := s.Install("u1", makeZip(t, map[string]string{"meta.json": echoMeta, "main.py": echoMain}, ""), "echo-two", false)
	if st, _ := r.VenvStatus(m2, s.Dir("u1", "echo-two")); st != VenvReady {
		t.Fatalf("second script's venv = %s, want the shared one ready", st)
	}
	venvs, _ := os.ReadDir(filepath.Join(data, "cache", "venvs"))
	if len(venvs) != 1 {
		t.Fatalf("venvs = %d, want 1", len(venvs))
	}
}
