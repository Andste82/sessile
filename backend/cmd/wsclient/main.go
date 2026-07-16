// Command wsclient is a tiny WebSocket test client for scripts/wstest.sh.
// It attaches to a session, optionally sends input, then prints all bytes
// received within a window. Used to verify the M1 WS protocol by hand.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "", "ws URL, e.g. ws://localhost:8080/ws/sessions/<id>")
	input := flag.String("input", "", "optional input to send (binary), e.g. 'echo hi\\n'")
	dur := flag.Duration("duration", 1500*time.Millisecond, "how long to read output")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "usage: wsclient -url ws://... [-input 'cmd\\n'] [-duration 2s]")
		os.Exit(2)
	}

	c, _, err := websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	if *input != "" {
		// Interpret a literal trailing \n.
		payload := unescape(*input)
		// Small delay so the shell is ready to read.
		time.Sleep(200 * time.Millisecond)
		if err := c.WriteMessage(websocket.BinaryMessage, []byte(payload)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}

	// A single read deadline: once it fires, the gorilla connection is
	// permanently errored, so we stop the loop on any read error.
	_ = c.SetReadDeadline(time.Now().Add(*dur))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			break
		}
		switch mt {
		case websocket.TextMessage:
			fmt.Printf("[control] %s\n", data)
		case websocket.BinaryMessage:
			os.Stdout.Write(data)
		}
	}
}

func unescape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				out = append(out, '\n')
				i++
				continue
			case 't':
				out = append(out, '\t')
				i++
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}
