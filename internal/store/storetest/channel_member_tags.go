package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func ChannelMemberTags(t *testing.T, channels store.ChannelStore, owner, member int64) {
	t.Helper()
	ctx := context.Background()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Unicode tags", Megagroup: true, MemberUserIDs: []int64{member}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	set := func(rank string) error {
		_, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{UserID: owner, ChannelID: id, MemberID: member, Rank: rank, Date: baseNow() + 1})
		return err
	}
	valid := strings.Repeat("角", 16)
	if err := set(valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, rank string
		want       error
	}{
		{"17 Unicode characters", strings.Repeat("角", 17), domain.ErrChannelRankInvalid},
		{"supplementary emoji", "🤖", domain.ErrChannelRankEmojiNotAllowed},
		{"keycap sequence", "#️⃣", domain.ErrChannelRankEmojiNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := set(tc.rank); !errors.Is(err, tc.want) {
				t.Fatalf("rank error=%v, want %v", err, tc.want)
			}
			stored, err := channels.GetParticipant(ctx, owner, id, member)
			if err != nil || stored.Rank != valid {
				t.Fatalf("failed update changed rank: %+v err=%v", stored, err)
			}
		})
	}
	if err := set("team #123*"); err != nil {
		t.Fatalf("ordinary ASCII rejected: %v", err)
	}
	if err := set(""); err != nil {
		t.Fatal(err)
	}
	stored, err := channels.GetParticipant(ctx, owner, id, member)
	if err != nil || stored.Rank != "" {
		t.Fatalf("rank was not cleared: %+v err=%v", stored, err)
	}
}
