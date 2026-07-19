package dlq

import (
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
)

func memoFixture(t *testing.T, kv map[string]string) *commonpb.Memo {
	t.Helper()
	fields := map[string]*commonpb.Payload{}
	for k, v := range kv {
		p, err := converter.GetDefaultDataConverter().ToPayload(v)
		if err != nil {
			t.Fatalf("payload: %v", err)
		}
		fields[k] = p
	}
	return &commonpb.Memo{Fields: fields}
}

func TestMemoString(t *testing.T) {
	m := memoFixture(t, map[string]string{"tenant": "payments"})
	if got := memoString(m, "tenant"); got != "payments" {
		t.Fatalf("want payments, got %q", got)
	}
	if got := memoString(m, "missing"); got != "" {
		t.Fatalf("missing key must be empty, got %q", got)
	}
	if got := memoString(nil, "tenant"); got != "" {
		t.Fatalf("nil memo must be empty, got %q", got)
	}
}

func TestMemoToMap(t *testing.T) {
	m := memoFixture(t, map[string]string{"tenant": "payments", "channel": "email"})
	got := memoToMap(m)
	if got["tenant"] != "payments" || got["channel"] != "email" {
		t.Fatalf("memoToMap: %+v", got)
	}
	if memoToMap(nil) != nil {
		t.Fatal("nil memo -> nil map")
	}
}

func TestParseProviderFromTaskQueue_ChannelPrefixed(t *testing.T) {
	if got := parseProviderFromTaskQueue("email-mailgun-payments-queue"); got != "mailgun-payments" {
		t.Fatalf("got %q", got)
	}
	if got := parseProviderFromTaskQueue("sms-twilio-queue"); got != "twilio" {
		t.Fatalf("got %q", got)
	}
}
