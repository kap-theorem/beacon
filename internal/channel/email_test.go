package channel

import (
	"strings"
	"testing"
)

func TestEmailDecodeRequest_Valid(t *testing.T) {
	ch := NewEmailChannel()
	req, err := ch.DecodeRequest([]byte(`{
		"to": "a@b.com", "cc": ["c@d.com"], "subject": "s", "body": "b",
		"html": true, "provider": "ses"
	}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Provider != "ses" {
		t.Fatalf("provider: %q", req.Provider)
	}
	n := req.Notification
	if n.Channel != "email" || n.Email == nil {
		t.Fatalf("bad envelope: %+v", n)
	}
	if n.Email.To != "a@b.com" || !n.Email.HTML || n.Email.CC[0] != "c@d.com" {
		t.Fatalf("bad payload: %+v", n.Email)
	}
}

func TestEmailDecodeRequest_Rejections(t *testing.T) {
	ch := NewEmailChannel()
	cases := []struct{ name, body, wantSubstr string }{
		{"bad json", `{`, "invalid request body"},
		{"missing to", `{"subject":"s"}`, "missing required field: to"},
		{"bad to", `{"to":"nope","subject":"s"}`, "invalid email address: to"},
		{"missing subject", `{"to":"a@b.com"}`, "missing required field: subject"},
		{"bad cc", `{"to":"a@b.com","subject":"s","cc":["nope"]}`, "cc/bcc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ch.DecodeRequest([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("want error containing %q, got %v", tc.wantSubstr, err)
			}
		})
	}
}

func TestEmailChannelMetadata(t *testing.T) {
	ch := NewEmailChannel()
	if ch.Name() != "email" {
		t.Fatalf("name: %q", ch.Name())
	}
	if got := ch.TaskQueue("sendgrid"); got != "email-sendgrid-queue" {
		t.Fatalf("task queue: %q", got)
	}
	if ch.WorkflowName() != "SendEmailWorkflow" {
		t.Fatalf("workflow: %q", ch.WorkflowName())
	}
}
