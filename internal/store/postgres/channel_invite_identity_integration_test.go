package postgres

import (
	"context"
	"sync"
	"testing"

	"telesrv/deploy"
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestChannelInviteIdentityPostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 1)
	storetest.ChannelInviteIdentity(t, NewChannelStore(pool), ids[0])
}

func TestInvitePermanentIdentityMigrationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ids := memberRulesUsers(t, pool, 2)
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], Title: "Legacy invites", Megagroup: true, MemberUserIDs: ids[1:], Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	main, err := channels.EnsurePermanentInvite(ctx, id, ids[0], 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	first, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: ids[0], ChannelID: id, Title: "Additional", Date: 1700000100})
	if err != nil {
		t.Fatal(err)
	}
	second, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: ids[0], ChannelID: id, Title: "Revoked", Date: 1700000200})
	if err != nil {
		t.Fatal(err)
	}
	down, err := deploy.Migrations.ReadFile("migrations/20261028010100_invite_permanent_identity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_invites SET permanent=true, revoked=(invite_id=$2) WHERE channel_id=$1`, id, second.Invite.InviteID); err != nil {
		t.Fatal(err)
	}
	up, err := deploy.Migrations.ReadFile("migrations/20261028010100_invite_permanent_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hash               string
		permanent, revoked bool
	}{{main.Hash, true, false}, {first.Invite.Hash, false, false}, {second.Invite.Hash, true, true}} {
		invite, err := channels.GetExportedInvite(ctx, domain.GetChannelInviteRequest{UserID: ids[0], ChannelID: id, Hash: tc.hash})
		if err != nil {
			t.Fatal(err)
		}
		if invite.Permanent != tc.permanent || invite.Revoked != tc.revoked {
			t.Fatalf("repaired invite = %+v, want permanent=%v revoked=%v", invite, tc.permanent, tc.revoked)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_invites SET permanent=true WHERE channel_id=$1 AND invite_id=$2`, id, first.Invite.InviteID); err == nil {
		t.Fatal("database accepted a second active main invite")
	}
}

func TestInviteMainRotationConcurrentPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ids := memberRulesUsers(t, pool, 1)
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], Title: "Concurrent rotation", Megagroup: true, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: ids[0], ChannelID: created.Channel.ID, LegacyRevokePermanent: true, Date: 1700000100})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := channels.ListExportedInvites(ctx, domain.ChannelInviteListRequest{UserID: ids[0], ChannelID: created.Channel.ID, AdminUserID: ids[0], Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, invite := range list.Invites {
		if invite.Permanent && !invite.Revoked {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active main links=%d, want 1", active)
	}
}
