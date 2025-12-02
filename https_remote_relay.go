// remote_relay_ws.go 

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// Simple JSON connect message (first message from client)
type connectCmd struct {
	Cmd    string `json:"cmd"`    // "connect"
	Target string `json:"target"` // e.g. "example.com:443"
	// Optionally you can add auth token fields here.
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// NOTE: Change this to stricter origin checks or add auth in production
		return true
	},
}

func wsRelayHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("New WebSocket connection from", r.RemoteAddr)
	// Upgrade to websocket
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer ws.Close()

	// Expect first message to be a small JSON connect command
	mt, msg, err := ws.ReadMessage()
	if err != nil {
		log.Println("read initial message error:", err)
		return
	}
	if mt != websocket.TextMessage {
		log.Println("initial message must be text JSON")
		return
	}

	// parse JSON (lightweight manual parse to avoid extra deps)
	var target string
	// very small JSON parse without importing encoding/json for speed example:
	// but we'll use encoding/json for correctness:
	type connReq struct {
		Cmd    string `json:"cmd"`
		Target string `json:"target"`
	}
	var cr connReq
	if err := json.Unmarshal(msg, &cr); err != nil {
		log.Println("invalid initial JSON:", err)
		return
	}
	if cr.Cmd != "connect" || cr.Target == "" {
		log.Println("invalid connect command")
		return
	}
	target = cr.Target

	log.Printf("WS client requested connect to %s\n", target)

	// Dial the target TCP address (raw)
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	targetConn, err := dialer.Dial("tcp", target)
	if err != nil {
		log.Println("dial target error:", err)
		// send back an error frame (text)
		_ = ws.WriteMessage(websocket.TextMessage, []byte("error: "+err.Error()))
		return
	}
	defer targetConn.Close()

	// Send an "ok" text frame so client knows the remote connection succeeded
	_ = ws.WriteMessage(websocket.TextMessage, []byte("connected"))

	// Now relay binary frames <-> targetConn
	errc := make(chan error, 2)

	// WS -> targetConn
	go func() {
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if mt != websocket.BinaryMessage {
				// ignore non-binary or treat some control frames
				continue
			}
			_, err = targetConn.Write(data)
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	// targetConn -> WS
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				if err != io.EOF {
					errc <- err
				} else {
					errc <- io.EOF
				}
				return
			}
			if n > 0 {
				// write as binary frame
				if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	// Wait for an error or connection close
	err = <-errc
	if err != nil && err != io.EOF {
		log.Println("relay error:", err)
	}
	log.Println("closing websocket relay for", target)
}

func main() {
	http.HandleFunc("/ws", wsRelayHandler)

	addr := ":8443" // recommended to run behind TLS reverse proxy (nginx) or use certs
	fmt.Println("Remote relay websocket server listening on", addr)
	// In production, run behind TLS or use ListenAndServeTLS with certs.
	log.Fatal(http.ListenAndServe(addr, nil))
}
