package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestEditWebPage(t *testing.T) {
	storetest.EditWebPage(t, func(t *testing.T) storetest.EditWebPageFixture {
		return storetest.EditWebPageFixture{
			Messages:  memory.NewMessageStore(memory.NewDialogStore()),
			Channels:  memory.NewChannelStore(),
			Owner:     101,
			Recipient: 102,
		}
	})
}
