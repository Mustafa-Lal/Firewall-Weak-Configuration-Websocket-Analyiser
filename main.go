// client_proxy.go
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding",
	"TE", "Trailer", "Upgrade", "Proxy-Authenticate", "Proxy-Authorization",
}


const REMOTE_RELAY_URL = "https://yourremoterelayendpointforhttp" 

type relayRequest struct {
	Method     string              `json:"method"`
	URL        string              `json:"url"`
	Headers    map[string][]string `json:"headers"`
	BodyBase64 string              `json:"body_base64"`
	RemoteAddr string              `json:"remote_addr"`
}

type relayResponse struct {
	Status     int                 `json:"status"`
	Headers    map[string][]string `json:"headers"`
	BodyBase64 string              `json:"body_base64"`
}

// sanitizeHeaders copies headers while skipping hop-by-hop headers.
func sanitizeHeaders(src http.Header) map[string][]string {
	out := make(map[string][]string)
skipLoop:
	for name, values := range src {
		lname := strings.ToLower(name)
		for _, h := range hopHeaders {
			if strings.ToLower(h) == lname {
				continue skipLoop
			}
		}
		out[name] = append([]string{}, values...)
	}
	return out
}

// buildDest builds a full URL string for the outgoing request.
func buildDestString(r *http.Request) string {
	dest := r.RequestURI
	// If RequestURI is not absolute, build from Host + Path
	if dest == "" || strings.HasPrefix(dest, "/") {
		u := &url.URL{
			Scheme: "http",
			Host:   r.Host,
			Path:   r.URL.Path,
		}
		u.RawQuery = r.URL.RawQuery
		dest = u.String()
	}
	return dest
}

func modifyAndRelayRequest(w http.ResponseWriter, r *http.Request) {
	// Read and preserve body
	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed reading body: "+err.Error(), http.StatusBadRequest)
			return
		}
		bodyBytes = b
		// restore r.Body so other code can read if needed
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	// Destination URL string
	dest := buildDestString(r)

	// Sanitized headers
	newHeaders := sanitizeHeaders(r.Header)

	// Ensure Host header present
	if r.Host != "" {
		newHeaders["Host"] = []string{r.Host}
	}

	// Add/append X-Forwarded-For
	if r.RemoteAddr != "" {
		if prev, ok := newHeaders["X-Forwarded-For"]; ok && len(prev) > 0 {
			newHeaders["X-Forwarded-For"] = []string{strings.Join(prev, ", ") + ", " + r.RemoteAddr}
		} else {
			newHeaders["X-Forwarded-For"] = []string{r.RemoteAddr}
		}
	}

	// Add Via
	if prev, ok := newHeaders["Via"]; ok && len(prev) > 0 {
		newHeaders["Via"] = []string{strings.Join(prev, ", ") + ", 1.1 MyGoProxy"}
	} else {
		newHeaders["Via"] = []string{"1.1 MyGoProxy"}
	}

	// Build relay request payload
	payload := relayRequest{
		Method:     r.Method,
		URL:        dest,
		Headers:    newHeaders,
		BodyBase64: base64.StdEncoding.EncodeToString(bodyBytes),
		RemoteAddr: r.RemoteAddr,
	}

	// Marshal JSON
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "failed to marshal relay payload: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Send to remote relay
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodPost, REMOTE_RELAY_URL, bytes.NewReader(data))
	if err != nil {
		http.Error(w, "failed creating request to relay: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	relayResp, err := client.Do(req)
	if err != nil {
		http.Error(w, "error contacting remote relay: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer relayResp.Body.Close()

	// Read relay response body
	respData, err := io.ReadAll(relayResp.Body)
	if err != nil {
		http.Error(w, "failed reading relay response: "+err.Error(), http.StatusBadGateway)
		return
	}

	var rresp relayResponse
	if err := json.Unmarshal(respData, &rresp); err != nil {
		http.Error(w, "invalid relay response: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Remove hop-by-hop response headers (safety)
	for _, h := range hopHeaders {
		delete(rresp.Headers, h)
	}

	// Write headers to original client
	for name, vals := range rresp.Headers {
		for _, v := range vals {
			w.Header().Add(name, v)
		}
	}

	// Decode body and write status + body
	bodyOut, _ := base64.StdEncoding.DecodeString(rresp.BodyBase64)
	w.WriteHeader(rresp.Status)
	_, _ = w.Write(bodyOut)
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Incoming: %s %s from %s\n", r.Method, r.RequestURI, r.RemoteAddr)
	// Forward to remote relay which will contact origin
	modifyAndRelayRequest(w, r)
}

func handleHTTPS(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("CONNECT (tunnel) requested: %s from %s\n", r.Host, r.RemoteAddr)
	 HandleHTTPSConnect(w, r)
}

func mainHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		handleHTTPS(w, r)
	} else {
		handleHTTP(w, r)
	}
}

func main() {
	addr := ":8080"
	fmt.Println("Client-side proxy listening on", addr)
	if err := http.ListenAndServe(addr, http.HandlerFunc(mainHandler)); err != nil {
		fmt.Println("Server error:", err)
	}
}
