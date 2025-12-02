// https_tunnel.go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	// "net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const REMOTE_WS_URL = "wss://fred-conjunction-calculated-champagne.trycloudflare.com/ws"

type connectCmd struct {
	Cmd    string `json:"cmd"`
	Target string `json:"target"`

}

func HandleHTTPSConnect(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("[HTTPS-REMOTE] CONNECT %s from %s\n", r.Host, r.RemoteAddr)

	// Hijack client connection (get raw tcp connection)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// From here we manage clientConn directly. Don't return without closing it.
	defer clientConn.Close()

	// Establish websocket (WSS) connection to remote relay
	// Use a websocket dialer, default config uses system root certs for TLS.
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		// You can set TLSClientConfig if needed.
	}

	// Build connect command JSON
	cmd := connectCmd{
		Cmd:    "connect",
		Target: r.Host, // e.g. example.com:443
	}
	cmdBytes, _ := json.Marshal(cmd)

	// Dial remote ws
	ws, _, err := dialer.Dial(REMOTE_WS_URL, nil)
	if err != nil {
		log.Println("dial remote ws error:", err)
		// inform client proxy failure
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	// ensure websocket is closed eventually
	defer ws.Close()

	// Send initial connect command
	if err := ws.WriteMessage(websocket.TextMessage, cmdBytes); err != nil {
		log.Println("ws write connect error:", err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	// Read response (expecting "connected" or error text)
	_, msg, err := ws.ReadMessage()
	if err != nil {
		log.Println("ws initial read error:", err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	// if the remote replied with "connected", we proceed.
	if string(msg) == "connected" {
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	} else {
		// remote returned an error string
		log.Println("remote error:", string(msg))
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	// Now wire up bidirectional copying:
	errc := make(chan error, 2)

	// client -> remote websocket (read raw bytes from client and send binary frames)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := clientConn.Read(buf)
			if err != nil {
				errc <- err
				return
			}
			if n > 0 {
				if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	// remote websocket -> client (read binary frames and write to clientConn)
	go func() {
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if mt != websocket.BinaryMessage {
				// ignore or handle text messages (keep-alive) if needed
				continue
			}
			if len(data) > 0 {
				_, err := clientConn.Write(data)
				if err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	// Wait until one side returns error/EOF
	err = <-errc
	if err != nil && err != io.EOF {
		log.Println("tunnel error:", err)
	}
	// cleanup happens via deferred closes
}
