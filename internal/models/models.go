package models

import "time"

// EmailMessage is the legacy /notify/email request payload.
// Deprecated: replaced by Notification; deleted at cutover.
type EmailMessage struct {
	To         string `json:"to"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	ClientHint string `json:"client_hint,omitempty"`
}

// EmailPayload is the email-channel payload inside a Notification.
// From* fields are set server-side from service policy — never from the request.
type EmailPayload struct {
	To          string   `json:"to"`
	CC          []string `json:"cc,omitempty"`
	BCC         []string `json:"bcc,omitempty"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	HTML        bool     `json:"html,omitempty"`
	FromAddress string   `json:"from_address,omitempty"`
	FromName    string   `json:"from_name,omitempty"`
}

// Notification is the channel-neutral envelope persisted as Temporal workflow
// input. Exactly one channel payload pointer is set.
type Notification struct {
	Channel   string        `json:"channel"`
	Service   string        `json:"service"`
	Tenant    string        `json:"tenant"`
	Provider  string        `json:"provider"`
	CreatedAt time.Time     `json:"created_at"`
	Email     *EmailPayload `json:"email,omitempty"`

	// Legacy fields decode pre-v2 workflow inputs (EmailMessage shape).
	// Remove one release after cutover.
	LegacyTo      string `json:"to,omitempty"`
	LegacySubject string `json:"subject,omitempty"`
	LegacyBody    string `json:"body,omitempty"`
}

// Normalize upgrades a legacy EmailMessage-shaped input to the envelope form.
func (n *Notification) Normalize() {
	if n.Email == nil && n.LegacyTo != "" {
		n.Email = &EmailPayload{To: n.LegacyTo, Subject: n.LegacySubject, Body: n.LegacyBody}
		if n.Channel == "" {
			n.Channel = "email"
		}
	}
}
