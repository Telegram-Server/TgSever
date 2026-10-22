package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

// TestSuggestedProfilePhotoOfferBackfillMigrationPostgres 守护 #38 的回填：上线前已送达的
// 建议头像消息，收件人得到与 OfferProfilePhoto 相同形状的停用历史行；建议者、拉黑了建议者的
// 收件人、已删除该消息的收件人都拿不到；任何人的当前头像与可见相册不变；重复执行幂等。
func TestSuggestedProfilePhotoOfferBackfillMigrationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	media := NewMediaStore(pool)
	messages := NewMessageStore(pool)

	nonce := time.Now().UnixNano() % 1_000_000
	suggester := createTestUser(t, ctx, users, fmt.Sprintf("15681%06d", nonce), "Suggest", "Er")
	recipient := createTestUser(t, ctx, users, fmt.Sprintf("15682%06d", nonce), "Recip", "Ient")
	blocker := createTestUser(t, ctx, users, fmt.Sprintf("15683%06d", nonce), "Block", "Er")
	deleter := createTestUser(t, ctx, users, fmt.Sprintf("15684%06d", nonce), "Delete", "Er")
	everyone := []int64{suggester.ID, recipient.ID, blocker.ID, deleter.ID}

	const base = int64(9100000000000000400)
	suggestedID, blockedID, deletedID, acceptedID := base+1, base+2, base+3, base+4
	recipientAvatarID, suggesterAvatarID, messagePhotoID := base+5, base+6, base+7
	photos := map[int64]domain.Photo{}
	for _, id := range []int64{suggestedID, blockedID, deletedID, acceptedID, recipientAvatarID, suggesterAvatarID, messagePhotoID} {
		photo := domain.Photo{ID: id, AccessHash: 7, Date: 1700000400, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 100, H: 100, Size: 1000}}}
		if err := media.PutPhoto(ctx, photo); err != nil {
			t.Fatalf("put photo %d: %v", id, err)
		}
		photos[id] = photo
	}
	if err := media.AddProfilePhotoKind(ctx, domain.PeerTypeUser, suggester.ID, domain.ProfilePhotoKindProfile, suggesterAvatarID, 1700000401); err != nil {
		t.Fatalf("seed suggester avatar: %v", err)
	}
	// 收件人之前已经接受过一条建议（acceptedID，仍在相册里），之后又换了新头像；
	// 回填不能把这一行改成停用。
	if err := media.AddProfilePhotoKind(ctx, domain.PeerTypeUser, recipient.ID, domain.ProfilePhotoKindProfile, acceptedID, 1700000402); err != nil {
		t.Fatalf("seed accepted suggestion: %v", err)
	}
	if err := media.AddProfilePhotoKind(ctx, domain.PeerTypeUser, recipient.ID, domain.ProfilePhotoKindProfile, recipientAvatarID, 1700000403); err != nil {
		t.Fatalf("seed recipient avatar: %v", err)
	}

	send := func(to int64, randomID int64, date int, blocked bool, m *domain.MessageMedia) domain.SendPrivateTextResult {
		t.Helper()
		res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID: suggester.ID, RecipientUserID: to, RandomID: randomID,
			Media: m, Date: date, RecipientBlocked: blocked,
		})
		if err != nil {
			t.Fatalf("send %d to %d: %v", randomID, to, err)
		}
		return res
	}
	suggest := func(to int64, randomID int64, date int, blocked bool, photoID int64) domain.SendPrivateTextResult {
		photo := photos[photoID]
		return send(to, randomID, date, blocked, &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind:  domain.MessageServiceActionSuggestProfilePhoto,
				Photo: &photo,
			},
		})
	}
	suggest(recipient.ID, nonce*10+1, 1700000500, false, suggestedID)
	// 同一张照片被建议了两次：只补一行，取较早的日期。
	suggest(recipient.ID, nonce*10+2, 1700000600, false, suggestedID)
	suggest(recipient.ID, nonce*10+3, 1700000510, false, acceptedID)
	suggest(blocker.ID, nonce*10+4, 1700000520, true, blockedID)
	deletedRes := suggest(deleter.ID, nonce*10+5, 1700000530, false, deletedID)
	messagePhoto := photos[messagePhotoID]
	send(recipient.ID, nonce*10+6, 1700000540, false, &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &messagePhoto})
	if _, err := pool.Exec(ctx, `UPDATE message_boxes SET deleted = true WHERE owner_user_id = $1 AND box_id = $2`, deleter.ID, deletedRes.RecipientMessage.ID); err != nil {
		t.Fatalf("delete recipient box: %v", err)
	}

	// 模拟上线前的库：发送路径如今会在送达后写 offer 行，回填前先把它们清掉，
	// 只保留上面显式种下的头像历史。
	if _, err := pool.Exec(ctx, `DELETE FROM profile_photos WHERE owner_peer_type = 'user' AND owner_peer_id = ANY($1) AND NOT active AND photo_id <> $2`, everyone, acceptedID); err != nil {
		t.Fatalf("reset offers: %v", err)
	}
	before := profilePhotoRows(t, ctx, pool, everyone)

	raw, err := deploy.Migrations.ReadFile("migrations/20261028010000_suggested_profile_photo_offers.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(raw)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	after := profilePhotoRows(t, ctx, pool, everyone)
	want := before + fmt.Sprintf("%d profile %d date=1700000500 active=false sort=0\n", recipient.ID, suggestedID)
	if after != sortedLines(want) {
		t.Fatalf("profile_photos after backfill:\n%s\nwant:\n%s", after, sortedLines(want))
	}

	if _, err := pool.Exec(ctx, string(raw)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again := profilePhotoRows(t, ctx, pool, everyone); again != after {
		t.Fatalf("profile_photos after second pass:\n%s\nwant unchanged:\n%s", again, after)
	}

	for owner, wantPhoto := range map[int64]int64{suggester.ID: suggesterAvatarID, recipient.ID: recipientAvatarID} {
		if cur, ok, err := media.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner, domain.ProfilePhotoKindProfile); err != nil || !ok || cur != wantPhoto {
			t.Fatalf("current avatar of %d = %d ok=%v err=%v, want %d", owner, cur, ok, err, wantPhoto)
		}
	}
	if _, total, err := media.ListProfilePhotosKind(ctx, domain.PeerTypeUser, recipient.ID, domain.ProfilePhotoKindProfile, 0, 10, 0); err != nil || total != 2 {
		t.Fatalf("recipient album total = %d err=%v, want the 2 photos it already had", total, err)
	}
	for _, tc := range []struct {
		name  string
		owner int64
		photo int64
		want  bool
	}{
		{"recipient gets the suggestion", recipient.ID, suggestedID, true},
		{"suggester does not", suggester.ID, suggestedID, false},
		{"recipient who blocked the suggester", blocker.ID, blockedID, false},
		{"recipient who deleted the suggestion", deleter.ID, deletedID, false},
		{"ordinary photo message", recipient.ID, messagePhotoID, false},
	} {
		got, err := media.ProfilePhotoInHistory(ctx, domain.PeerTypeUser, tc.owner, tc.photo)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: in history = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// profilePhotoRows 把 owners 的全部 profile_photos 行渲染成排好序的文本，便于整体比较。
func profilePhotoRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owners []int64) string {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT owner_peer_id, kind, photo_id, date, active, sort_order
FROM profile_photos
WHERE owner_peer_type = 'user' AND owner_peer_id = ANY($1)`, owners)
	if err != nil {
		t.Fatalf("query profile_photos: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var owner, photo, sortOrder int64
		var kind string
		var date int32
		var active bool
		if err := rows.Scan(&owner, &kind, &photo, &date, &active, &sortOrder); err != nil {
			t.Fatalf("scan profile_photos: %v", err)
		}
		fmt.Fprintf(&b, "%d %s %d date=%d active=%v sort=%d\n", owner, kind, photo, date, active, sortOrder)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate profile_photos: %v", err)
	}
	return sortedLines(b.String())
}

func sortedLines(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}
