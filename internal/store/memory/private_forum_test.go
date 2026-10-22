package memory

import (
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestPrivateForumLifecycleIsolation(t *testing.T) {
	messages := NewMessageStore(NewDialogStore())
	channels := NewChannelStore()
	AttachMessageReplyStores(messages, channels)
	storetest.PrivateForum(t, messages, func() store.MessageStore { return messages }, channels, 11, 22, 33)
}
