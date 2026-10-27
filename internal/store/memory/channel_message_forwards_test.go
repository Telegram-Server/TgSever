package memory_test

import (
	"testing"

	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestChannelMessageForwards(t *testing.T) {
	storetest.ChannelMessageForwards(t, func(t *testing.T) storetest.ChannelMessageForwardsFixture {
		channels := memory.NewChannelStore()
		messages := memory.NewMessageStore(memory.NewDialogStore())
		messages.AttachChannelStore(channels)
		return storetest.ChannelMessageForwardsFixture{
			Messages: messages,
			Channels: channels,
			Owner:    201,
			Reader:   202,
		}
	})
}
