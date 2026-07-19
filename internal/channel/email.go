package channel

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"beacon/internal/models"
)

type emailChannel struct{}

// NewEmailChannel returns the email channel implementation.
func NewEmailChannel() Channel { return emailChannel{} }

func (emailChannel) Name() string { return "email" }

func (emailChannel) TaskQueue(provider string) string { return TaskQueue("email", provider) }

func (emailChannel) WorkflowName() string { return "SendEmailWorkflow" }

const maxCopyRecipients = 50

type emailRequest struct {
	To       string   `json:"to"`
	CC       []string `json:"cc"`
	BCC      []string `json:"bcc"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	HTML     bool     `json:"html"`
	Provider string   `json:"provider"`
}

func (emailChannel) DecodeRequest(body []byte) (*Request, error) {
	var req emailRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid request body")
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		return nil, fmt.Errorf("missing required field: to")
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return nil, fmt.Errorf("invalid email address: to")
	}
	if req.Subject == "" {
		return nil, fmt.Errorf("missing required field: subject")
	}
	if len(req.CC)+len(req.BCC) > maxCopyRecipients {
		return nil, fmt.Errorf("too many recipients in cc/bcc (max %d)", maxCopyRecipients)
	}
	for _, addr := range req.CC {
		if _, err := mail.ParseAddress(addr); err != nil {
			return nil, fmt.Errorf("invalid email address in cc: %s", addr)
		}
	}
	for _, addr := range req.BCC {
		if _, err := mail.ParseAddress(addr); err != nil {
			return nil, fmt.Errorf("invalid email address in bcc: %s", addr)
		}
	}
	return &Request{
		Provider: req.Provider,
		Notification: &models.Notification{
			Channel: "email",
			Email: &models.EmailPayload{
				To: to, CC: req.CC, BCC: req.BCC,
				Subject: req.Subject, Body: req.Body, HTML: req.HTML,
			},
		},
	}, nil
}
