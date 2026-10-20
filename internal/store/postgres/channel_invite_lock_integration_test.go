package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestChannelInviteLockNamespacePostgres(t *testing.T) {
	pool := testPool(t)
	ids := memberRulesUsers(t, pool, 1)
	channels := NewChannelStore(pool)
	for _, operation := range []string{"export", "ensure", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], Title: "Invite lock " + operation, Megagroup: true, Date: 1700000000})
			if err != nil {
				t.Fatal(err)
			}
			id := created.Channel.ID
			main, err := channels.EnsurePermanentInvite(ctx, id, ids[0], 1700000001)
			if err != nil {
				t.Fatal(err)
			}
			invoke := func(ctx context.Context) error {
				switch operation {
				case "export":
					_, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: ids[0], ChannelID: id, LegacyRevokePermanent: true, Date: 1700000010})
					return err
				case "ensure":
					_, err := channels.EnsurePermanentInvite(ctx, id, ids[0], 1700000010)
					return err
				default:
					_, err := channels.EditExportedInvite(ctx, domain.EditChannelInviteRequest{UserID: ids[0], ChannelID: id, Hash: main.Hash, Revoked: true, Date: 1700000010})
					return err
				}
			}
			// Real user/bot owner locks must not block any invitation operation.
			ownerTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer ownerTx.Rollback(context.Background())
			if _, err := ownerTx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, id); err != nil {
				t.Fatal(err)
			}
			independentCtx, independentCancel := context.WithTimeout(ctx, 5*time.Second)
			err = invoke(independentCtx)
			independentCancel()
			if err != nil {
				t.Fatalf("invitation operation blocked by user/bot lock: %v", err)
			}
			if err := ownerTx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			main, err = channels.EnsurePermanentInvite(ctx, id, ids[0], 1700000020)
			if err != nil {
				t.Fatal(err)
			}
			// All three public operations must wait behind the same channel gate.
			inviteTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer inviteTx.Rollback(context.Background())
			if err := lockChannelInvites(ctx, inviteTx, id); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- invoke(ctx) }()
			waiting := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if err := pool.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
AND classid = $1::oid AND objsubid = 2 AND NOT granted
)`, channelInviteAdvisoryNamespace).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-finished:
					t.Fatalf("operation bypassed channel invite lock: %v", err)
				case <-time.After(5 * time.Millisecond):
				}
			}
			if !waiting {
				t.Fatal("invitation operation never waited for the channel gate")
			}
			if err := inviteTx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			list, err := channels.ListExportedInvites(ctx, domain.ChannelInviteListRequest{UserID: ids[0], ChannelID: id, AdminUserID: ids[0], Limit: 100})
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
				t.Fatalf("active permanent invitations = %d, want 1", active)
			}
		})
	}
}

func TestChannelInviteLockUsesFullChannelIDPostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	low, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer low.Rollback(context.Background())
	if err := lockChannelInvites(ctx, low, 7); err != nil {
		t.Fatal(err)
	}
	high, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer high.Rollback(context.Background())
	if err := lockChannelInvites(ctx, high, (int64(1)<<40)|7); err != nil {
		t.Fatalf("channel IDs with equal low 32 bits shared a lock: %v", err)
	}
}
