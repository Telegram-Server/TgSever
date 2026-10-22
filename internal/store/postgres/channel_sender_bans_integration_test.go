package postgres

import (
	"context"
	"sync"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelSenderBansPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 441, Phone: "+188" + suffix + "01", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 442, Phone: "+188" + suffix + "02", FirstName: "Member"})
	if err != nil {
		t.Fatal(err)
	}
	storetest.ChannelSenderBans(t, NewChannelStore(pool), func() store.ChannelStore { return NewChannelStore(pool) }, owner.ID, member.ID)
	t.Run("linked discussion auto-forward", func(t *testing.T) { storetest.ChannelSenderBanLinkedDiscussion(t, NewChannelStore(pool), owner.ID) })
}

func TestChannelSenderConcurrentUnbanPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ids := memberRulesUsers(t, pool, 1)
	channels := NewChannelStore(pool)
	group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], Title: "Concurrent unban", Megagroup: true, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: ids[0], Title: "Sender", Broadcast: true, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	req := domain.EditChannelBannedRequest{UserID: ids[0], ChannelID: group.Channel.ID, Participant: domain.Peer{Type: domain.PeerTypeChannel, ID: sender.Channel.ID}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: 1700000010}
	if _, err := channels.EditChannelBanned(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.BannedRights = domain.ChannelBannedRights{}
	const workers = 8
	start := make(chan struct{})
	results := make(chan domain.EditChannelBannedResult, workers)
	errs := make(chan error, workers)
	var groupWG sync.WaitGroup
	for range workers {
		groupWG.Add(1)
		go func() {
			defer groupWG.Done()
			<-start
			result, err := channels.EditChannelBanned(ctx, req)
			results <- result
			errs <- err
		}()
	}
	close(start)
	groupWG.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changed := 0
	for result := range results {
		if !result.NoChange {
			changed++
		}
	}
	logs, err := channels.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: ids[0], ChannelID: group.Channel.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	unbans := 0
	for _, event := range logs.Events {
		if event.Type == domain.ChannelAdminLogParticipantUnkick {
			unbans++
		}
	}
	if changed != 1 || unbans != 1 {
		t.Fatalf("concurrent unbans: transitions=%d audit=%d, want 1 each", changed, unbans)
	}
}
