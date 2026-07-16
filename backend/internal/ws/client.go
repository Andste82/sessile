package ws

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Client channel and timing parameters (§4.4, §5).
const (
	sendQueueSize  = 256
	writeWait      = 10 * time.Second
	pongWait       = 40 * time.Second // > ping period + expected pong window
	pingPeriod     = 30 * time.Second
	maxMessageSize = 1 << 20 // 1 MiB input frame cap
)

// frame is a queued outbound message; binary carries terminal bytes, otherwise
// it is a text JSON control message.
type frame struct {
	binary bool
	data   []byte
}

// Client is one attached WebSocket connection. It satisfies session.Client.
// A single write-pump goroutine is the only writer to the connection, as
// gorilla requires (§4.4).
type Client struct {
	id   string
	conn *websocket.Conn
	send chan frame

	closeOnce sync.Once
	done      chan struct{}

	// close frame details, set before done is closed.
	closeCode   int
	closeReason string
}

func newClient(conn *websocket.Conn) *Client {
	return &Client{
		id:   uuid.NewString(),
		conn: conn,
		send: make(chan frame, sendQueueSize),
		done: make(chan struct{}),
	}
}

// ID implements session.Client.
func (c *Client) ID() string { return c.id }

// Send enqueues binary terminal bytes. Non-blocking; false means the queue is
// full (slow consumer) and the caller should drop this client.
func (c *Client) Send(data []byte) bool {
	select {
	case c.send <- frame{binary: true, data: data}:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

// SendControl enqueues a JSON control message as a text frame.
func (c *Client) SendControl(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	select {
	case c.send <- frame{binary: false, data: b}:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

// Close signals the write pump to send a close frame and tear down. Idempotent.
func (c *Client) Close(code int, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeReason = reason
		close(c.done)
	})
}

// writePump is the sole writer to the connection: it drains send, emits pings,
// and writes a close frame when done fires (§4.4).
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case f := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			mt := websocket.BinaryMessage
			if !f.binary {
				mt = websocket.TextMessage
			}
			if err := c.conn.WriteMessage(mt, f.data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			code := c.closeCode
			if code == 0 {
				code = websocket.CloseNormalClosure
			}
			msg := websocket.FormatCloseMessage(code, c.closeReason)
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, msg)
			return
		}
	}
}
