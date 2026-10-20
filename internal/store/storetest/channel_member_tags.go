package storetest

import (
	"context"
	"errors"
	"reflect"
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

// ChannelMessageRanks verifies exact-pair batching and fresh metadata in both stores.
func ChannelMessageRanks(t *testing.T, channels store.ChannelStore, owner, member int64) {
	t.Helper()
	ctx := context.Background()
	create := func(title string) int64 {
		t.Helper()
		created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: title, Megagroup: true, MemberUserIDs: []int64{member}, Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		return created.Channel.ID
	}
	first, second := create("First rank page"), create("Second rank page")
	edit := func(channelID, userID int64, rank string) {
		t.Helper()
		if _, err := channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{UserID: owner, ChannelID: channelID, MemberID: userID, Rank: rank, Date: baseNow() + 1}); err != nil {
			t.Fatal(err)
		}
	}
	a := domain.ChannelMessageAuthor{ChannelID: first, UserID: member}
	b := domain.ChannelMessageAuthor{ChannelID: second, UserID: owner}
	missing := domain.ChannelMessageAuthor{ChannelID: first, UserID: -1}
	authors := []domain.ChannelMessageAuthor{a, b, a, missing}
	check := func(want map[domain.ChannelMessageAuthor]string) {
		t.Helper()
		got, err := channels.GetMessageRanks(ctx, authors)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("message ranks = %v, want %v", got, want)
		}
	}
	edit(first, member, "navigator")
	edit(second, owner, "captain")
	// Existing but unrequested cross-pairs must not appear in the result.
	edit(first, owner, "first owner")
	edit(second, member, "second member")
	check(map[domain.ChannelMessageAuthor]string{a: "navigator", b: "captain"})
	edit(first, member, "pilot")
	check(map[domain.ChannelMessageAuthor]string{a: "pilot", b: "captain"})
	edit(first, member, "")
	check(map[domain.ChannelMessageAuthor]string{b: "captain"})
	edit(first, member, "departing")
	if _, err := channels.LeaveChannel(ctx, first, member, baseNow()+2); err != nil {
		t.Fatal(err)
	}
	check(map[domain.ChannelMessageAuthor]string{b: "captain"})
	ranks, err := channels.GetMessageRanks(ctx, nil)
	if err != nil || len(ranks) != 0 {
		t.Fatalf("empty author batch = %v, err=%v", ranks, err)
	}
}
