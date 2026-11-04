package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

func TestSavedRingtoneContractPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createRevokeTestUser(t, ctx, pool, "ringtone-a")
	other := createRevokeTestUser(t, ctx, pool, "ringtone-b")
	media := NewMediaStore(pool)
	docs := [3]int64{user*10 + 1, user*10 + 2, user*10 + 3}
	for _, id := range docs {
		if err := media.PutDocument(ctx, domain.Document{ID: id, AccessHash: id, MimeType: "audio/mpeg", Size: 1, DCID: 2}); err != nil {
			t.Fatal(err)
		}
	}
	storetest.SavedRingtoneContract(t, NewPasswordStore(pool), user, other, docs)
}
