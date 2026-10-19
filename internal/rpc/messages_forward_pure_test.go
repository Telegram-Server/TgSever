package rpc

import (
	"slices"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// forwardGroupedIDs 的契约是分组之间的关系，不是某个具体的哈希值：同一源相册共享一个新分组、
// 不同源相册互不相同、非相册为 0、新分组为正、同样的输入（重试）得到同样的结果、
// 已重放条目的分组（inherited）优先于派生。断言关系而不是抄实现算出的数，换派生算法不该让它坏。
func TestForwardGroupedIDs(t *testing.T) {
	albumA := forwardSource{groupedID: 100}
	albumB := forwardSource{groupedID: 200}
	single := forwardSource{}

	t.Run("albums share within, differ across, non-album is 0", func(t *testing.T) {
		got := forwardGroupedIDs(42, []int64{1001, 1002, 1003, 1004, 1005},
			[]forwardSource{albumA, albumA, albumB, single, albumB}, nil)
		if got[0] <= 0 || got[2] <= 0 {
			t.Fatalf("groups = %v, want positive ids for albums", got)
		}
		if got[0] != got[1] || got[2] != got[4] {
			t.Fatalf("groups = %v, want each source album to share one id", got)
		}
		if got[0] == got[2] {
			t.Fatalf("groups = %v, want different albums to get different ids", got)
		}
		if got[3] != 0 {
			t.Fatalf("groups = %v, want 0 for the non-album message", got)
		}
	})

	t.Run("a retry yields the same groups", func(t *testing.T) {
		randomIDs := []int64{1001, 1002, 1003}
		sources := []forwardSource{albumA, albumA, albumB}
		first := forwardGroupedIDs(42, randomIDs, sources, nil)
		if again := forwardGroupedIDs(42, randomIDs, sources, nil); !slices.Equal(again, first) {
			t.Fatalf("retry = %v, want %v", again, first)
		}
	})

	t.Run("an inherited group wins over derivation", func(t *testing.T) {
		got := forwardGroupedIDs(42, []int64{2001, 2002, 2003},
			[]forwardSource{albumA, albumB, albumA}, map[int64]int64{100: 777})
		if got[0] != 777 || got[2] != 777 {
			t.Fatalf("groups = %v, want album A to keep the inherited 777", got)
		}
		if got[1] <= 0 || got[1] == 777 {
			t.Fatalf("groups = %v, want album B derived on its own", got)
		}
	})

	t.Run("no albums", func(t *testing.T) {
		if got := forwardGroupedIDs(42, []int64{3001, 3002}, []forwardSource{single, single}, nil); !slices.Equal(got, []int64{0, 0}) {
			t.Fatalf("groups = %v, want all 0", got)
		}
	})
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
