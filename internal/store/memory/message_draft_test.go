package memory

import (
	"telesrv/internal/store/storetest"
	"testing"
)

func TestMessageDraftLifecycle(t *testing.T) {
	storetest.MessageDraftLifecycle(t, NewMessageDraftStore(), 1000)
}
