package dlq

import (
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
)

// memoString decodes one string field from a workflow memo ("" if absent).
func memoString(memo *commonpb.Memo, key string) string {
	if memo == nil {
		return ""
	}
	p, ok := memo.GetFields()[key]
	if !ok {
		return ""
	}
	var s string
	if err := converter.GetDefaultDataConverter().FromPayload(p, &s); err != nil {
		return ""
	}
	return s
}

// memoToMap converts a memo to StartWorkflowOptions.Memo form so replays
// keep the original service/tenant/channel/provider visibility.
func memoToMap(memo *commonpb.Memo) map[string]interface{} {
	if memo == nil || len(memo.GetFields()) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(memo.GetFields()))
	for k := range memo.GetFields() {
		if v := memoString(memo, k); v != "" {
			out[k] = v
		}
	}
	return out
}
