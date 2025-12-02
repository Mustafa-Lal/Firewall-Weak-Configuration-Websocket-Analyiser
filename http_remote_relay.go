package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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

func relayHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received relay request from:", r.RemoteAddr)
	var relayReq relayRequest

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", 400)
		return
	}

	err = json.Unmarshal(body, &relayReq)
	if err != nil {
		http.Error(w, "json decode error", 400)
		return
	}

	reqBody, _ := base64.StdEncoding.DecodeString(relayReq.BodyBase64)

	// Prepare real request
	req, err := http.NewRequest(relayReq.Method, relayReq.URL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "build request error", 500)
		return
	}

	for k, vals := range relayReq.Headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "remote request error: "+err.Error(), 502)
		return
	}

	respBody, _ := io.ReadAll(resp.Body)

	relayResp := relayResponse{
		Status:     resp.StatusCode,
		Headers:    resp.Header,
		BodyBase64: base64.StdEncoding.EncodeToString(respBody),
	}

	jsonResp, _ := json.Marshal(relayResp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonResp)
}

func main() {
	http.HandleFunc("/relay", relayHandler)
	fmt.Println("Remote relay server running on :8081")
	http.ListenAndServe(":8081", nil)
}
