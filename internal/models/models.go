package models

type EmailMessage struct {
	To         string `json:"to"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	ClientHint string `json:"client_hint,omitempty"`
}
