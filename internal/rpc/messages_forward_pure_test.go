package rpc

import (
	"slices"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// forwardGroupedIDs 的期望值是字面量：派生分组 = sha256(userID || 该组第一条的 random_id)
// 前 8 字节去掉符号位，算法变了这里必须跟着显式更新。
func TestForwardGroupedIDs(t *testing.T) {
	const (
		user42Rid1001 = int64(4175778989593976167)
		user42Rid1003 = int64(8668281499685509150)
		user42Rid2002 = int64(806772489254850963)
	)
	albumA := forwardSource{groupedID: 100}
	albumB := forwardSource{groupedID: 200}
	single := forwardSource{}

	for _, tc := range []struct {
		name      string
		userID    int64
		randomIDs []int64
		sources   []forwardSource
		inherited map[int64]int64
		want      []int64
	}{
		{
			name:      "same source album shares, different albums differ, non-album is 0",
			userID:    42,
			randomIDs: []int64{1001, 1002, 1003, 1004, 1005},
			sources:   []forwardSource{albumA, albumA, albumB, single, albumB},
			want:      []int64{user42Rid1001, user42Rid1001, user42Rid1003, 0, user42Rid1003},
		},
		{
			name:      "inherited group wins over derivation",
			userID:    42,
			randomIDs: []int64{2001, 2002, 2003},
			sources:   []forwardSource{albumA, albumB, albumA},
			inherited: map[int64]int64{100: 777},
			want:      []int64{777, user42Rid2002, 777},
		},
		{
			name:      "no albums",
			userID:    42,
			randomIDs: []int64{3001, 3002},
			sources:   []forwardSource{single, single},
			want:      []int64{0, 0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := forwardGroupedIDs(tc.userID, tc.randomIDs, tc.sources, tc.inherited)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("forwardGroupedIDs = %v, want %v", got, tc.want)
			}
			// 同样的输入（重试）得到同样的分组。
			if again := forwardGroupedIDs(tc.userID, tc.randomIDs, tc.sources, tc.inherited); !slices.Equal(again, got) {
				t.Fatalf("retry = %v, want %v", again, got)
			}
		})
	}
}

func TestDropForwardMediaCaptions(t *testing.T) {
	entities := []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 1}}
	photo := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto}
	document := &domain.MessageMedia{Kind: domain.MessageMediaKindDocument}
	dice := &domain.MessageMedia{Kind: domain.MessageMediaKindDice}
	fresh := func() []forwardSource {
		return []forwardSource{
			{body: "photo caption", entities: entities, media: photo},
			{body: "doc caption", entities: entities, media: document},
			{body: "plain text", entities: entities},
			{body: "dice", entities: entities, media: dice},
		}
	}

	for _, tc := range []struct {
		name              string
		dropAuthor        bool
		dropMediaCaptions bool
		wantBodies        []string
	}{
		{name: "drop_author with drop_media_captions strips media captions", dropAuthor: true, dropMediaCaptions: true,
			wantBodies: []string{"", "", "plain text", "dice"}},
		{name: "drop_media_captions without drop_author does nothing", dropMediaCaptions: true,
			wantBodies: []string{"photo caption", "doc caption", "plain text", "dice"}},
		{name: "drop_author alone does nothing", dropAuthor: true,
			wantBodies: []string{"photo caption", "doc caption", "plain text", "dice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := fresh()
			dropForwardMediaCaptions(&tg.MessagesForwardMessagesRequest{DropAuthor: tc.dropAuthor, DropMediaCaptions: tc.dropMediaCaptions}, sources)
			for i, src := range sources {
				if src.body != tc.wantBodies[i] {
					t.Fatalf("source %d body = %q, want %q", i, src.body, tc.wantBodies[i])
				}
				if wantEntities := tc.wantBodies[i] != ""; (len(src.entities) != 0) != wantEntities {
					t.Fatalf("source %d entities = %+v, want kept = %v", i, src.entities, wantEntities)
				}
				if src.media != fresh()[i].media {
					t.Fatalf("source %d media changed to %+v", i, src.media)
				}
			}
		})
	}
}
