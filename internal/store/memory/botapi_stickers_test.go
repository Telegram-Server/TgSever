package memory

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestBotAPIStickerLifecycleMemory(t *testing.T) {
	media := NewMediaStore()
	storetest.BotAPIStickerLifecycle(t, func() store.MediaStore { return media })
}
