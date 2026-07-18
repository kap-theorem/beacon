package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

var (
	port    = flag.String("port", "8000", "Port to listen on")
	fail    = flag.Int("fail-count", 0, "Number of initial failures (for retry testing)")
	slowMs  = flag.Int("slow-ms", 0, "Add latency in milliseconds")
	badJson = flag.Bool("bad-json", false, "Return invalid JSON")
)

var requestCount atomic.Int64

func main() {
	flag.Parse()

	http.HandleFunc("/api/v4/secrets", handleSecrets)
	fmt.Printf("Mock Infisical server listening on :%s\n", *port)
	fmt.Printf("Config: fail-count=%d, slow-ms=%d, bad-json=%v\n", *fail, *slowMs, *badJson)

	if err := http.ListenAndServe(":"+*port, nil); err != nil {
		panic(err)
	}
}

func handleSecrets(w http.ResponseWriter, r *http.Request) {
	count := requestCount.Add(1)
	fmt.Printf("[%d] %s %s\n", count, r.Method, r.RequestURI)

	if int(count) <= *fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "Service temporarily unavailable")
		return
	}

	if *badJson {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{invalid json}`)
		return
	}

	secrets := []map[string]string{
		{
			"secretKey": "sendgrid",
			"secretValue": mustMarshal(map[string]any{
				"name":         "sendgrid",
				"provider":     "sendgrid",
				"host":         "smtp.sendgrid.net",
				"port":         587,
				"username":     "apikey",
				"password":     "SG.test-key-12345",
				"auth_type":    "PLAIN",
				"tls":          map[string]any{"enabled": true, "server_name": "smtp.sendgrid.net"},
				"timeout":      "30s",
				"max_retries":  3,
				"max_per_hour": 0,
			}),
		},
		{
			"secretKey": "mailgun",
			"secretValue": mustMarshal(map[string]any{
				"name":        "mailgun",
				"provider":    "mailgun",
				"host":        "smtp.mailgun.org",
				"port":        587,
				"username":    "postmaster@mg.example.com",
				"password":    "mg-test-key",
				"auth_type":   "PLAIN",
				"tls":         map[string]any{"enabled": true, "server_name": "smtp.mailgun.org"},
				"timeout":     "25s",
				"max_retries": 2,
			}),
		},
		{
			"secretKey": "aws-ses",
			"secretValue": mustMarshal(map[string]any{
				"name":      "ses",
				"provider":  "aws-ses",
				"host":      "email-smtp.us-east-1.amazonaws.com",
				"port":      587,
				"username":  "AKIAIOSFODNN7EXAMPLE",
				"password":  "test-smtp-password",
				"auth_type": "LOGIN",
				"tls":       map[string]any{"enabled": true, "server_name": "email-smtp.us-east-1.amazonaws.com"},
			}),
		},
	}

	response := map[string]any{
		"secrets": secrets,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
	fmt.Printf("[%d] Response sent (secrets: %d)\n", count, len(secrets))
}

func mustMarshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	// Remove all whitespace to ensure compact JSON
	result := strings.ReplaceAll(string(data), " ", "")
	result = strings.ReplaceAll(result, "\n", "")
	result = strings.ReplaceAll(result, "\t", "")
	return result
}
