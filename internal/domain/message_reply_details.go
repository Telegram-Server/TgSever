package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type MessageReplyDetails struct {
	Version         int                   `json:"version"`
	External        *MessageReplyExternal `json:"external,omitempty"`
	ChecklistTaskID int                   `json:"checklist_task_id,omitempty"`
	PollOption      []byte                `json:"poll_option,omitempty"`
	ForumTopic      bool                  `json:"forum_topic,omitempty"`
}

func EncodeMessageReplyDetails(reply *MessageReply) ([]byte, error) {
	if reply == nil || reply.External == nil && reply.ChecklistTaskID == 0 && len(reply.PollOption) == 0 && !reply.ForumTopic {
		return []byte("{}"), nil
	}
	if reply.ChecklistTaskID < 0 || reply.ChecklistTaskID > MaxMessageBoxID || len(reply.PollOption) > 256 || reply.ChecklistTaskID != 0 && len(reply.PollOption) != 0 {
		return nil, ErrReplyMessageIDInvalid
	}
	if _, err := EncodeMessageReplyExternal(reply.External); err != nil {
		return nil, err
	}
	out, err := json.Marshal(MessageReplyDetails{Version: 1, External: reply.External, ChecklistTaskID: reply.ChecklistTaskID, PollOption: reply.PollOption, ForumTopic: reply.ForumTopic})
	if len(out) > MaxMessageReplyExternalBytes+4096 {
		return nil, ErrReplyMessageIDInvalid
	}
	return out, err
}
func DecodeMessageReplyDetails(data []byte) (MessageReplyDetails, error) {
	var out MessageReplyDetails
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("{}")) {
		return out, nil
	}
	if len(data) > MaxMessageReplyExternalBytes+4096 {
		return out, ErrReplyMessageIDInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, err
	}
	if decoder.Decode(new(any)) != io.EOF || out.Version != 1 {
		return out, fmt.Errorf("invalid reply details version")
	}
	if _, err := EncodeMessageReplyDetails(&MessageReply{External: out.External, ChecklistTaskID: out.ChecklistTaskID, PollOption: out.PollOption, ForumTopic: out.ForumTopic}); err != nil {
		return out, err
	}
	return out, nil
}
