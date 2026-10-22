package postgres

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotAPIStickerLifecyclePostgres(t *testing.T) {
	pool := testPool(t)
	storetest.BotAPIStickerLifecycle(t, func() store.MediaStore { return NewMediaStore(pool) })
}
