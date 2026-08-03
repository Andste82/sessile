package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestWritePumpFlushesQueuedFramesBeforeClose pins the rule the exit frame
// depends on: everything enqueued before Close is written before the close
// frame.
//
// Sending a control message and then closing is one action in two steps — a
// stopped session tells its clients why (§5) — but they are two channel
// operations, and writePump used to choose between them at random whenever
// both were ready. This test forces exactly that state: the queue is filled and
// done is closed before the pump ever runs, so its first select sees both. With
// the old code the pump had to win a coin flip per frame to deliver them all;
// with the flush it always does.
func TestWritePumpFlushesQueuedFramesBeforeClose(t *testing.T) {
	const (
		queued    = 8
		closeCode = 4000
	)

	served := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c := newClient(conn)
		for i := 0; i < queued; i++ {
			if !c.SendControl(map[string]any{"type": "exit", "seq": i}) {
				t.Errorf("enqueue %d rejected", i)
			}
		}
		c.Close(closeCode, "session ended")
		c.writePump()
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got []int
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if !errors.As(err, &ce) {
				t.Fatalf("read: %v", err)
			}
			if ce.Code != closeCode {
				t.Errorf("close code = %d, want %d", ce.Code, closeCode)
			}
			break
		}
		var msg struct {
			Type string `json:"type"`
			Seq  int    `json:"seq"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal %q: %v", data, err)
		}
		if msg.Type != "exit" {
			t.Errorf("frame type = %q, want exit", msg.Type)
		}
		got = append(got, msg.Seq)
	}

	if len(got) != queued {
		t.Fatalf("delivered %d of %d queued frames before the close: %v", len(got), queued, got)
	}
	for i, seq := range got {
		if seq != i {
			t.Fatalf("frames delivered out of order: %v", got)
		}
	}
	<-served
}
