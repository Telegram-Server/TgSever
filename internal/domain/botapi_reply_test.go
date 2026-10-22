package domain

import "testing"

func TestBotAPIReplyQuoteUTF16FormattingAndTaskSelection(t *testing.T) {
	source := Message{Body: "😀hello hello", Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 2, Length: 5}}}
	valid := &MessageReply{MessageID: 1, QuoteText: "hello", QuoteOffset: 2, QuoteEntities: []MessageEntity{{Type: MessageEntityBold, Offset: 0, Length: 5}}}
	if err := ValidateBotAPIReplySource(valid, source); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []*MessageReply{
		{MessageID: 1, QuoteText: "hello", QuoteOffset: 1},
		{MessageID: 1, QuoteText: "hello", QuoteOffset: 2},
		{MessageID: 1, QuoteText: "hello", QuoteOffset: 8, QuoteEntities: valid.QuoteEntities},
		{MessageID: 1, QuoteText: "hello", QuoteOffset: int(^uint(0) >> 1)},
	} {
		if err := ValidateBotAPIReplySource(reply, source); err == nil {
			t.Fatalf("invalid quote accepted: %+v", reply)
		}
	}
	source.Media = &MessageMedia{Kind: MessageMediaKindTodo, Todo: &MessageTodo{Title: "List", Items: []MessageTodoItem{{ID: 7, Title: "buy milk"}}}}
	task := &MessageReply{MessageID: 1, ChecklistTaskID: 7, QuoteText: "milk", QuoteOffset: 4}
	if err := ValidateBotAPIReplySource(task, source); err != nil {
		t.Fatal(err)
	}
	task.ChecklistTaskID = 8
	if err := ValidateBotAPIReplySource(task, source); err == nil {
		t.Fatal("missing task accepted")
	}
}
func TestPrivateReplyDetailsPreservesTaskAndOption(t *testing.T) {
	for _, reply := range []*MessageReply{{MessageID: 1, ChecklistTaskID: 7}, {MessageID: 1, PollOption: []byte{1, 2}}} {
		encoded, err := EncodeMessageReplyDetails(reply)
		if err != nil {
			t.Fatal(err)
		}
		details, err := DecodeMessageReplyDetails(encoded)
		if err != nil || details.ChecklistTaskID != reply.ChecklistTaskID || string(details.PollOption) != string(reply.PollOption) {
			t.Fatalf("details lost: %+v %v", details, err)
		}
	}
	if _, err := DecodeMessageReplyDetails([]byte(`{"version":99}`)); err == nil {
		t.Fatal("unknown version accepted")
	}
}
