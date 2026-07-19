package app

import "testing"

func TestParseWorkerSpec(t *testing.T) {
	cases := []struct {
		spec, wantCh, wantProv string
		wantErr                bool
	}{
		{"email-sendgrid", "email", "sendgrid", false},
		{"email-mailgun-payments", "email", "mailgun-payments", false},
		{"sms-twilio", "sms", "twilio", false},
		{"email", "", "", true},
		{"", "", "", true},
		{"-sendgrid", "", "", true},
		{"email-", "", "", true},
		{"email--x", "email", "-x", false},
	}
	for _, tc := range cases {
		ch, prov, err := ParseWorkerSpec(tc.spec)
		if tc.wantErr != (err != nil) {
			t.Fatalf("%q: err=%v", tc.spec, err)
		}
		if ch != tc.wantCh || prov != tc.wantProv {
			t.Fatalf("%q: got (%q,%q)", tc.spec, ch, prov)
		}
	}
}
