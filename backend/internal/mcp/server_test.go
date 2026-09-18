package mcp

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/scripts"
	"github.com/Andste82/sessile/backend/internal/storage"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

const toolMeta = `{"name":"tix","version":"1.0.0","description":"Ticket tracker","settings":[
 {"name":"TIX_URL","label":"URL","type":"url","required":true,"context":true},
 {"name":"TIX_TOKEN","label":"Token","type":"secret","required":true}],
 "guidance":["Read tickets before planning."],
 "functions":[
 {"name":"read","effect":"read","description":"Read a ticket","input":{"type":"object","required":["key"],"properties":{"key":{"type":"string"}}}},
 {"name":"comment","effect":"write","description":"Comment on a ticket"}]}`

const toolMain = `import json, os, sys
data = json.load(sys.stdin)
print(json.dumps({"fn": sys.argv[1], "input": data, "token": os.environ["TIX_TOKEN"]}))
`

type recorder struct {
	mu   sync.Mutex
	msgs []any
}

func (r *recorder) publish(_ string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, v)
}

func (r *recorder) find(pred func(any) bool) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.msgs {
		if pred(m) {
			return m
		}
	}
	return nil
}

type client struct {
	t  *testing.T
	c  net.Conn
	r  *bufio.Scanner
	id int
}

func (c *client) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	if _, err := c.c.Write(append(req, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for c.r.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(c.r.Bytes(), &msg); err != nil {
			c.t.Fatal(err)
		}
		if id, ok := msg["id"].(float64); ok && int(id) == c.id {
			return msg
		}
	}
	c.t.Fatalf("no reply to %s", method)
	return nil
}

func setup(t *testing.T) (*Server, *recorder, string, net.Listener) {
	t.Helper()
	if err := exec.Command("python3", "-c", "import venv, ensurepip").Run(); err != nil {
		t.Skip("python3 without venv")
	}
	data := t.TempDir()
	db, err := storage.Open(filepath.Join(data, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := &tasks.Service{DB: db, Agents: agents.NewRegistry(data), Hosts: hosts.NewRegistry(data)}
	taskID := "demo-abcdef"
	spec, _ := json.Marshal(tasks.Spec{Name: "Demo", Target: "local", Agent: tasks.AgentSpec{ProfileID: "p"}})
	if err := db.InsertTask(storage.TaskRow{ID: taskID, SessionID: "s1", UserID: "u1", SpecJSON: string(spec), Created: time.Now()}); err != nil {
		t.Fatal(err)
	}

	store := scripts.NewStore(data)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"meta.json": toolMeta, "main.py": toolMain} {
		w, _ := zw.Create(name)
		io.WriteString(w, body)
	}
	zw.Close()
	if _, err := store.Install("u1", buf.Bytes(), "", false); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSettings("u1", "tix", scripts.Settings{Values: map[string]string{"TIX_URL": "https://tix.example", "TIX_TOKEN": "tok-12345"}}); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(store, scripts.NewRunner(data, log), svc, rec.publish, log)

	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.Serve(l, "u1", taskID, "the-token")
	return s, rec, taskID, l
}

func connect(t *testing.T, l net.Listener, token string) *client {
	t.Helper()
	c, err := net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, "SESSILE-TOKEN %s\n", token)
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	return &client{t: t, c: c, r: sc}
}

func TestWrongTokenIsDropped(t *testing.T) {
	_, _, _, l := setup(t)
	c := connect(t, l, "nope")
	c.c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
	c.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if c.r.Scan() {
		t.Fatalf("got a reply with a wrong token: %s", c.r.Text())
	}
}

func TestToolsEndToEnd(t *testing.T) {
	s, rec, taskID, l := setup(t)
	c := connect(t, l, "the-token")

	init := c.call("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}})
	if res := init["result"].(map[string]any); res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize = %v", init)
	}

	list := c.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	names := map[string]string{}
	for _, tl := range list {
		m := tl.(map[string]any)
		names[m["name"].(string)] = m["description"].(string)
	}
	if !strings.Contains(names["tix__read"], "URL: https://tix.example") {
		t.Fatalf("tix__read description = %q", names["tix__read"])
	}
	if _, ok := names["set_task_summary"]; !ok {
		t.Fatal("no set_task_summary")
	}
	for name, desc := range names {
		if strings.Contains(desc, "tok-12345") {
			t.Fatalf("%s's description leaks the secret", name)
		}
	}

	// A read runs straight away, and its output is redacted.
	res := c.call("tools/call", map[string]any{"name": "tix__read", "arguments": map[string]any{"key": "T-1"}})["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] == true || !strings.Contains(text, `"T-1"`) || strings.Contains(text, "tok-12345") {
		t.Fatalf("read result = %v", res)
	}
	// Schema-invalid input is refused before anything runs.
	res = c.call("tools/call", map[string]any{"name": "tix__read", "arguments": map[string]any{}})["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("invalid input ran: %v", res)
	}

	// A write waits for the user: approve one, deny the next.
	for _, approve := range []bool{true, false} {
		done := make(chan map[string]any, 1)
		go func() {
			done <- c.call("tools/call", map[string]any{"name": "tix__comment", "arguments": map[string]any{}})["result"].(map[string]any)
		}()
		var callID string
		deadline := time.Now().Add(5 * time.Second)
		for callID == "" && time.Now().Before(deadline) {
			if m := rec.find(func(v any) bool {
				a, ok := v.(ApprovalMsg)
				return ok && a.Status == "pending" && !strings.Contains(fmt.Sprint(rec.msgs), a.CallID+" "+"approved")
			}); m != nil {
				callID = m.(ApprovalMsg).CallID
			}
			time.Sleep(20 * time.Millisecond)
		}
		if callID == "" {
			t.Fatal("no approval request published")
		}
		if err := s.Decide("u2", taskID, callID, true); err == nil {
			t.Fatal("another user decided the call")
		}
		if err := s.Decide("u1", taskID, callID, approve); err != nil {
			t.Fatal(err)
		}
		r := <-done
		if approve && r["isError"] == true {
			t.Fatalf("approved write failed: %v", r)
		}
		if !approve && (r["isError"] != true || !strings.Contains(fmt.Sprint(r), "denied")) {
			t.Fatalf("denied write ran: %v", r)
		}
		rec.mu.Lock()
		rec.msgs = nil
		rec.mu.Unlock()
	}

	res = c.call("tools/call", map[string]any{"name": "set_task_summary", "arguments": map[string]any{"summary": "PR #1 open"}})["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("set_task_summary: %v", res)
	}
	got, _ := s.Tasks.Get("u1", taskID)
	if got.Summary != "PR #1 open" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if c.call("nope", nil)["error"] == nil {
		t.Fatal("unknown method didn't error")
	}
	if section := s.ToolsSection("u1"); !strings.Contains(section, "tix__comment (write)") || !strings.Contains(section, "Read tickets before planning.") {
		t.Fatalf("tools section = %s", section)
	}
}
