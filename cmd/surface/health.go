package main

import (
	"encoding/json"
	"net/http"
)

// healthz serves GET /healthz.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// version serves GET /api/version: the commit the binary was built from.
func version(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		SHA string `json:"sha"`
	}{commitSHA})
}
