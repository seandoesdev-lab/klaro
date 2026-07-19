package main

import (
	"net/http"
	"os"
)

// A tiny target server for manual e2e. If MODE=fail it always 500s,
// exercising the circuit breaker.
func main() {
	mode := os.Getenv("MODE")
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if mode == "fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":9000"
	}
	_ = http.ListenAndServe(addr, nil)
}
