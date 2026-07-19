package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sync/atomic"
)

var (
	port    = flag.String("port", "8000", "Port to listen on")
	fail    = flag.Int("fail-count", 0, "Number of initial failures (for retry testing)")
	badJson = flag.Bool("bad-json", false, "Return invalid JSON")
)

var requestCount atomic.Int64

func main() {
	flag.Parse()

	http.HandleFunc("/api/v4/secrets", handleSecrets)
	fmt.Printf("Mock Infisical server listening on :%s\n", *port)
	fmt.Printf("Config: fail-count=%d, bad-json=%v\n", *fail, *badJson)

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

	var secrets []map[string]string
	switch r.URL.Query().Get("secretPath") {
	case "/beacon/providers/email":
		secrets = []map[string]string{
			{
				"secretKey": "sendgrid",
				"secretValue": mustMarshal(map[string]any{
					"name":       "sendgrid",
					"provider":   "sendgrid",
					"host":       "smtp.sendgrid.net",
					"port":       587,
					"username":   "apikey",
					"password":   "SG.test-key-12345",
					"auth_type":  "PLAIN",
					"tls":        map[string]any{"enabled": true, "server_name": "smtp.sendgrid.net"},
					"timeout":    "30s",
					"is_default": true,
				}),
			},
			{
				"secretKey": "mailgun",
				"secretValue": mustMarshal(map[string]any{
					"name":      "mailgun",
					"provider":  "mailgun",
					"host":      "smtp.mailgun.org",
					"port":      587,
					"username":  "postmaster@mg.example.com",
					"password":  "mg-test-key",
					"auth_type": "PLAIN",
					"tls":       map[string]any{"enabled": true, "server_name": "smtp.mailgun.org"},
					"timeout":   "25s",
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
	case "/beacon/tenants":
		secrets = []map[string]string{
			{
				"secretKey": "payments",
				"secretValue": mustMarshal(map[string]any{
					"tenant": "payments",
					"name":   "Payments Team",
				}),
			},
		}
	case "/beacon/services":
		secrets = []map[string]string{
			{
				"secretKey": "billing-api",
				"secretValue": mustMarshal(map[string]any{
					"service": "billing-api",
					"tenant":  "payments",
					"enabled": true,
					"keys": []map[string]any{
						{"id": "k1", "sha256": sha256Hex("bk_k1_local-test-key"), "state": "active"},
					},
					"channels": map[string]any{
						"email": map[string]any{
							"providers":        []string{"sendgrid"},
							"default_provider": "sendgrid",
							"from": map[string]any{
								"address": "billing@example.com",
								"name":    "Billing",
							},
							"rate": map[string]any{"rpm": 60, "daily": 5000},
						},
					},
				}),
			},
		}
	default:
		secrets = []map[string]string{}
	}

	response := map[string]any{
		"secrets": secrets,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
	fmt.Printf("[%d] Response sent (secrets: %d)\n", count, len(secrets))
}

// mustMarshal encodes v as compact JSON. json.Marshal already produces the
// most compact encoding (no incidental whitespace between tokens), so this
// just guards against any future switch to an indenting encoder; it must NOT
// strip whitespace from within string values (e.g. "Payments Team").
func mustMarshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		panic(err)
	}
	return buf.String()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
