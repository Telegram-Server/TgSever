package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

func TestSuggestedPostLifecycleProjection(t *testing.T) {
	msg := domain.Message{ID: 12, SuggestedPost: &domain.SuggestedPost{Accepted: true, ScheduleDate: 1700000200, Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceTON, Amount: 10000000}}}
	out := map[string]any{}
	projectSuggestedPost(msg, out)
	info := out["suggested_post_info"].(map[string]any)
	price := info["price"].(map[string]any)
	if info["state"] != "approved" || info["send_date"] != 1700000200 || price["currency"] != "TON" || price["amount"] != int64(10000000) {
		t.Fatalf("suggested_post_info=%+v", info)
	}
	for _, tc := range []struct {
		action     domain.ChannelMessageAction
		field, key string
		want       any
	}{
		{domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostRejected: true, SuggestedPostRejectComment: "Different audience"}, "suggested_post_declined", "comment", "Different audience"},
		{domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund}, "suggested_post_refunded", "reason", "post_deleted"},
		{domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund, SuggestedPostPayerInitiated: true}, "suggested_post_refunded", "reason", "payment_refunded"},
	} {
		out := map[string]any{}
		projectSuggestedPost(domain.Message{Action: &tc.action}, out)
		service, ok := out[tc.field].(map[string]any)
		if !ok || service[tc.key] != tc.want {
			t.Fatalf("%s=%+v", tc.field, out)
		}
	}
}
