package push

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// forum 话题的通知设置优先于群组自身，群组再优先于默认范围；话题没设的字段继承群组。
func TestSettingsPolicyConsultsForumTopicBeforeGroup(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	const groupID, topicID = int64(70), 3
	groupScope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: groupID}}
	topicScope := groupScope
	topicScope.TopicID = topicID
	cases := []struct {
		name         string
		group, topic *domain.PeerNotifySettings
		want         NotifyDecision
	}{
		{"topic muted, group not", nil, &domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)}, NotifyDecision{Mute: true}},
		{"group muted, topic unmuted", &domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)}, &domain.PeerNotifySettings{MuteUntil: intPtr(0)}, NotifyDecision{}},
		{"group muted, topic unset inherits", &domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)}, nil, NotifyDecision{Mute: true}},
		{"topic hides preview, group mute inherited", &domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)}, &domain.PeerNotifySettings{ShowPreviews: boolPtr(false)}, NotifyDecision{Mute: true, HidePreview: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := memory.NewPasswordStore()
			if tc.group != nil {
				if err := settings.SaveNotifySettings(ctx, bobID, groupScope, *tc.group); err != nil {
					t.Fatal(err)
				}
			}
			if tc.topic != nil {
				if err := settings.SaveNotifySettings(ctx, bobID, topicScope, *tc.topic); err != nil {
					t.Fatal(err)
				}
			}
			policy := NewSettingsPolicy(settings, func() time.Time { return now })
			got, err := policy.Decide(ctx, bobID, Chat{Kind: ChatGroup, ID: groupID, TopicID: topicID})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Decide = %+v, want %+v", got, tc.want)
			}
			// 话题之外的消息不受话题设置影响。
			outside, err := policy.Decide(ctx, bobID, Chat{Kind: ChatGroup, ID: groupID})
			if err != nil {
				t.Fatal(err)
			}
			wantOutside := NotifyDecision{Mute: tc.group != nil}
			if outside != wantOutside {
				t.Fatalf("Decide outside the topic = %+v, want %+v", outside, wantOutside)
			}
		})
	}
}
