package storetest

import (
	"context"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BusinessConnections exercises the same durable delegation lifecycle on both backends.
func BusinessConnections(t *testing.T, s store.BusinessAutomationStore, ownerID, botID, otherBotID int64) {
	t.Helper()
	ctx := context.Background()
	input := domain.ConnectedBusinessBot{OwnerUserID: ownerID, BotUserID: botID, Recipients: domain.BusinessBotRecipients{Users: []int64{otherBotID}}, Rights: domain.BusinessBotRights{Reply: true}}
	first, err := s.SaveConnectedBusinessBot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConnectionID == "" || first.Disabled {
		t.Fatalf("connection not active: %+v", first)
	}
	first.Recipients.Users[0] = ownerID
	saved, found, err := s.GetBusinessBotConnection(ctx, first.ConnectionID)
	if err != nil || !found || saved.Recipients.Users[0] != otherBotID {
		t.Fatalf("snapshot alias or missing: %+v %v %v", saved, found, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			update := input
			update.Rights.ReadMessages = true
			current, err := s.SaveConnectedBusinessBot(ctx, update)
			if err != nil || current.ConnectionID != first.ConnectionID {
				t.Errorf("concurrent update rotated connection: %+v %v", current, err)
			}
		}()
	}
	wg.Wait()
	saved, found, err = s.GetBusinessBotConnection(ctx, first.ConnectionID)
	if err != nil || !found || !saved.Rights.ReadMessages {
		t.Fatalf("updated rights missing: %+v %v %v", saved, found, err)
	}
	if _, err := s.SetConnectedBusinessBotPaused(ctx, ownerID, otherBotID, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeleteConnectedBusinessBot(ctx, ownerID, otherBotID); err != nil || ok {
		t.Fatalf("other bot revoked connection: %v %v", ok, err)
	}
	if ok, err := s.DeleteConnectedBusinessBot(ctx, ownerID, botID); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if _, found, err := s.GetConnectedBusinessBot(ctx, ownerID); err != nil || found {
		t.Fatalf("revoked connection remains current: %v %v", found, err)
	}
	revoked, found, err := s.GetBusinessBotConnection(ctx, first.ConnectionID)
	if err != nil || !found || !revoked.Disabled {
		t.Fatalf("revocation snapshot missing: %+v %v %v", revoked, found, err)
	}
	second, err := s.SaveConnectedBusinessBot(ctx, input)
	if err != nil || second.ConnectionID == first.ConnectionID || second.Disabled {
		t.Fatalf("reconnect reused revoked capability: %+v %v", second, err)
	}
	if _, found, err := s.GetConnectedBusinessBotPeerState(ctx, ownerID, otherBotID); err != nil || found {
		t.Fatalf("reconnect inherited peer pause: %v %v", found, err)
	}
	input.BotUserID = otherBotID
	third, err := s.SaveConnectedBusinessBot(ctx, input)
	if err != nil || third.ConnectionID == second.ConnectionID {
		t.Fatalf("replacement reused capability: %+v %v", third, err)
	}
	revoked, found, err = s.GetBusinessBotConnection(ctx, second.ConnectionID)
	if err != nil || !found || !revoked.Disabled {
		t.Fatalf("replaced bot remains active: %+v %v %v", revoked, found, err)
	}
	cleanup, ok := s.(interface {
		DeleteBusinessConnectionsForUser(context.Context, int64) error
	})
	if !ok {
		t.Fatal("business store lacks account-deletion cleanup")
	}
	if err := cleanup.DeleteBusinessConnectionsForUser(ctx, ownerID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ConnectionID, second.ConnectionID, third.ConnectionID} {
		if _, found, err := s.GetBusinessBotConnection(ctx, id); err != nil || found {
			t.Fatalf("deleted account retained connection %s: %v %v", id, found, err)
		}
	}
	if _, found, err := s.GetConnectedBusinessBot(ctx, ownerID); err != nil || found {
		t.Fatalf("deleted account retained current bot: %v %v", found, err)
	}
}
