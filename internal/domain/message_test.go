package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMessageRichMessageMissingBlocksLayerIsLegacy228(t *testing.T) {
	var rich MessageRichMessage
	if err := json.Unmarshal([]byte(`{"blocks":"FQ=="}`), &rich); err != nil {
		t.Fatal(err)
	}
	if got := rich.EffectiveBlocksLayer(); got != MessageRichBlocksLegacyLayer {
		t.Fatalf("effective blocks layer = %d, want %d", got, MessageRichBlocksLegacyLayer)
	}
	if rich.BlocksLayer != 0 {
		t.Fatalf("legacy JSON mutated stored blocks layer to %d", rich.BlocksLayer)
	}
}

func TestMessageRichMessageExplicitBlocksLayer(t *testing.T) {
	const storedLayer = 230
	rich := MessageRichMessage{BlocksLayer: storedLayer}
	if got := rich.EffectiveBlocksLayer(); got != storedLayer {
		t.Fatalf("effective blocks layer = %d, want %d", got, storedLayer)
	}
}

func TestValidateMessageReplyBoundsRejectsQuoteOffsetAsTextOffset(t *testing.T) {
	reply := &MessageReply{
		MessageID:   1,
		QuoteText:   "hello",
		QuoteOffset: MaxMessageReplyQuoteOffset + 1,
	}
	if err := ValidateMessageReplyBounds(reply); !errors.Is(err, ErrReplyMessageIDInvalid) {
		t.Fatalf("ValidateMessageReplyBounds err = %v, want ErrReplyMessageIDInvalid", err)
	}
}

func TestValidateMessageReplyBoundsAllowsForumTopicOnlyHeader(t *testing.T) {
	reply := &MessageReply{
		TopMessageID: 10,
		ForumTopic:   true,
	}
	if err := ValidateMessageReplyBounds(reply); err != nil {
		t.Fatalf("ValidateMessageReplyBounds err = %v, want nil", err)
	}
}

func TestValidateMessageReplyBoundsForSourceDetails(t *testing.T) {
	tests := []struct {
		name  string
		reply MessageReply
		valid bool
	}{
		{"checklist lower bound", MessageReply{MessageID: 1, ChecklistTaskID: -1}, false},
		{"checklist upper bound", MessageReply{MessageID: 1, ChecklistTaskID: MaxMessageBoxID}, true},
		{"checklist overflow", MessageReply{MessageID: 1, ChecklistTaskID: MaxMessageBoxID + 1}, false},
		{"poll option limit", MessageReply{MessageID: 1, PollOption: make([]byte, 256)}, true},
		{"poll option overflow", MessageReply{MessageID: 1, PollOption: make([]byte, 257)}, false},
		{"mutually exclusive details", MessageReply{MessageID: 1, ChecklistTaskID: 1, PollOption: []byte{1}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMessageReplyBounds(&test.reply)
			if test.valid && err != nil {
				t.Fatalf("valid source detail rejected: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrReplyMessageIDInvalid) {
				t.Fatalf("invalid source detail error = %v", err)
			}
		})
	}
}
