package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

// TestChannelMemberRestrictionActorBackfillMigrationPostgres 守护 #167 的回填：升级前限制写入时把
// 执行的管理员记在 inviter_user_id，没有限制时间。回填后被踢出、不在群里但受限的记录执行人取
// 邀请人字段、时间取离开时间；在群受限的执行人取邀请人字段、时间保持 0；没有限制的记录（含只带
// 到期时间、没有任何限制项的记录）两列保持 0。重复执行幂等。
func TestChannelMemberRestrictionActorBackfillMigrationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ids := memberRulesUsers(t, pool, 7)
	owner, admin, muted, kicked, outsider, plain, left := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6]
	s := NewChannelStore(pool)
	now := nowUnix()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Restriction backfill", Megagroup: true,
		MemberUserIDs: []int64{admin, muted, kicked, outsider, plain, left}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: admin, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	restrict := func(target int64, rights domain.ChannelBannedRights, date int) {
		t.Helper()
		if _, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: admin, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: date}); err != nil {
			t.Fatal(err)
		}
	}
	leave := func(target int64, date int) {
		t.Helper()
		if _, err := s.LeaveChannel(ctx, id, target, date); err != nil {
			t.Fatal(err)
		}
	}
	mute := domain.ChannelBannedRights{SendMessages: true}
	restrict(muted, mute, now+10)
	restrict(kicked, domain.ChannelBannedRights{ViewMessages: true}, now+20)
	leave(outsider, now+30)
	restrict(outsider, mute, now+40)
	leave(left, now+50)

	// 模拟升级前的库：限制写入改写了邀请人、没有执行人与时间两列的值。
	if _, err := pool.Exec(ctx, `
UPDATE channel_members
SET restricted_by_user_id = 0,
    restricted_at = 0,
    inviter_user_id = CASE WHEN user_id = ANY($2::bigint[]) THEN $3 ELSE inviter_user_id END
WHERE channel_id = $1`, id, []int64{muted, kicked, outsider, plain}, admin); err != nil {
		t.Fatalf("simulate legacy rows: %v", err)
	}
	// 升级前"只带到期时间、没有任何限制项"的请求也按受限写入（改写了邀请人）；它没有限制任何事，
	// 回填不把它当作受限。
	untilOnly, err := json.Marshal(domain.ChannelBannedRights{UntilDate: now + 1_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_members SET banned_rights = $3::jsonb WHERE channel_id = $1 AND user_id = $2`, id, plain, string(untilOnly)); err != nil {
		t.Fatalf("simulate legacy until-only row: %v", err)
	}

	raw, err := deploy.Migrations.ReadFile("migrations/20261101030000_channel_member_restriction_actor.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		fmt.Sprintf("%d active by=0 at=0", owner),
		fmt.Sprintf("%d active by=0 at=0", admin),
		fmt.Sprintf("%d active by=%d at=0", muted, admin),
		fmt.Sprintf("%d kicked by=%d at=%d", kicked, admin, now+20),
		fmt.Sprintf("%d banned by=%d at=%d", outsider, admin, now+30),
		fmt.Sprintf("%d active by=0 at=0", plain),
		fmt.Sprintf("%d left by=0 at=0", left),
	}, "\n") + "\n"
	for _, pass := range []string{"first pass", "second pass"} {
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		if got := restrictionActorRows(t, ctx, pool, id); got != want {
			t.Fatalf("%s: channel_members after backfill:\n%s\nwant:\n%s", pass, got, want)
		}
	}
}

func restrictionActorRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channelID int64) string {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT user_id, status, restricted_by_user_id, restricted_at
FROM channel_members
WHERE channel_id = $1
ORDER BY user_id`, channelID)
	if err != nil {
		t.Fatalf("query channel_members: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var userID, by int64
		var status string
		var at int32
		if err := rows.Scan(&userID, &status, &by, &at); err != nil {
			t.Fatalf("scan channel_members: %v", err)
		}
		fmt.Fprintf(&b, "%d %s by=%d at=%d\n", userID, status, by, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate channel_members: %v", err)
	}
	return sortedLines(b.String())
}
