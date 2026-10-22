package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const UpdateEventBotAPIObject UpdateEventType = "bot_api_object"

// BotAPIObject contains only a public Bot API object, never an account/session model.
// RawMessage makes the queued snapshot independent of mutable producer maps.
type BotAPIObject struct {
	Kind    BotAPIUpdateKind `json:"kind"`
	Payload json.RawMessage  `json:"payload"`
}

func BotAPIUpdateKindKnown(kind BotAPIUpdateKind) bool {
	switch kind {
	case BotAPIUpdateMessage, BotAPIUpdateEditedMessage, BotAPIUpdateCallbackQuery, BotAPIUpdatePreCheckoutQuery,
		BotAPIUpdateMyChatMember, BotAPIUpdateChatMember, BotAPIUpdateMessageReaction, BotAPIUpdateMessageReactionCount, BotAPIUpdateChatJoinRequest:
		return true
	default:
		return BotAPIObjectKind(kind)
	}
}

func BotAPIObjectKind(kind BotAPIUpdateKind) bool {
	switch kind {
	case "message", "edited_message", "chat_join_request", "channel_post", "edited_channel_post", "business_connection", "business_message", "edited_business_message", "deleted_business_messages", "guest_message", "inline_query", "chosen_inline_result", "shipping_query", "purchased_paid_media", "poll", "poll_answer", "chat_boost", "removed_chat_boost", "managed_bot", "subscription", "stopped_message_generation":
		return true
	}
	return false
}

func NewBotAPIObject(kind BotAPIUpdateKind, payload map[string]any) (*BotAPIObject, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	object := &BotAPIObject{Kind: kind, Payload: raw}
	if err := object.Validate(); err != nil {
		return nil, err
	}
	return object, nil
}

func (o *BotAPIObject) Clone() *BotAPIObject {
	if o == nil {
		return nil
	}
	return &BotAPIObject{Kind: o.Kind, Payload: append(json.RawMessage(nil), o.Payload...)}
}

func (o *BotAPIObject) Object() (map[string]any, error) {
	return o.validatedObject()
}

func decodeBotAPIObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("invalid bot api object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing bot api object")
	}
	return body, nil
}

var errBotAPIObjectInvalid = errors.New("invalid bot api public object")

func (o *BotAPIObject) Validate() error {
	_, err := o.validatedObject()
	return err
}

func (o *BotAPIObject) validatedObject() (map[string]any, error) {
	invalid := errBotAPIObjectInvalid
	if o == nil || !BotAPIObjectKind(o.Kind) || len(o.Payload) == 0 || len(o.Payload) > 1024*1024 {
		return nil, invalid
	}
	body, err := decodeBotAPIObject(o.Payload)
	fields := botAPIObjectFields(o.Kind)
	for key := range body {
		allowed := false
		for field := range strings.FieldsSeq(fields) {
			if key == field {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, invalid
		}
	}
	if err != nil || !publicBotAPIJSON(body) {
		return nil, invalid
	}
	text := func(key string, empty bool) bool {
		value, ok := body[key].(string)
		return ok && (empty || value != "")
	}
	number := func(key string, positive bool) bool {
		value, ok := body[key].(json.Number)
		if !ok {
			return false
		}
		n, err := value.Int64()
		return err == nil && (!positive || n > 0)
	}
	user := func(key string) bool { value, ok := body[key].(map[string]any); return ok && publicBotAPIUser(value) }
	chat := func() bool { value, ok := body["chat"].(map[string]any); return ok && publicBotAPIChat(value) }
	valid := false
	switch o.Kind {
	case "inline_query":
		valid = text("id", false) && user("from") && text("query", true) && text("offset", true)
	case "chosen_inline_result":
		valid = text("result_id", false) && user("from") && text("query", true)
	case "chat_join_request":
		valid = chat() && user("from") && number("user_chat_id", true) && number("date", true) && text("query_id", false)
	case "business_connection":
		_, enabled := body["is_enabled"].(bool)
		valid = text("id", false) && user("user") && number("user_chat_id", true) && number("date", true) && enabled
	case "deleted_business_messages":
		ids, ok := body["message_ids"].([]any)
		valid = text("business_connection_id", false) && chat() && ok && len(ids) > 0
		for _, value := range ids {
			n, ok := value.(json.Number)
			id, err := n.Int64()
			if !ok || err != nil || id <= 0 {
				valid = false
			}
		}
	case "message", "edited_message", "business_message", "edited_business_message", "channel_post", "edited_channel_post", "guest_message":
		valid = number("message_id", true) && number("date", false) && chat()
		if o.Kind == "business_message" || o.Kind == "edited_business_message" {
			valid = valid && text("business_connection_id", false)
		}
		if o.Kind == "guest_message" {
			valid = valid && text("guest_query_id", false)
		}
		if o.Kind == "channel_post" || o.Kind == "edited_channel_post" {
			c, _ := body["chat"].(map[string]any)
			valid = valid && c["type"] == "channel"
		}
	case "shipping_query":
		_, address := body["shipping_address"].(map[string]any)
		valid = text("id", false) && user("from") && text("invoice_payload", true) && address
	case "purchased_paid_media":
		valid = user("from") && text("paid_media_payload", false)
	case "poll":
		options, ok := body["options"].([]any)
		_, closed := body["is_closed"].(bool)
		_, anonymous := body["is_anonymous"].(bool)
		_, multiple := body["allows_multiple_answers"].(bool)
		valid = text("id", false) && text("question", false) && number("total_voter_count", false) && ok && len(options) >= 2 && closed && anonymous && multiple && (body["type"] == "regular" || body["type"] == "quiz")
		_, revoting := body["allows_revoting"].(bool)
		_, membersOnly := body["members_only"].(bool)
		valid = valid && revoting && membersOnly
		total, _ := body["total_voter_count"].(json.Number)
		count, err := total.Int64()
		valid = valid && err == nil && count >= 0
		for _, value := range options {
			option, ok := value.(map[string]any)
			label, labeled := option["text"].(string)
			persistent, identified := option["persistent_id"].(string)
			voters, counted := option["voter_count"].(json.Number)
			n, err := voters.Int64()
			if !ok || !labeled || label == "" || !identified || persistent == "" || !counted || err != nil || n < 0 {
				valid = false
			}
		}
	case "poll_answer":
		ids, ok := body["option_ids"].([]any)
		voter, voterChat := body["voter_chat"].(map[string]any)
		valid = text("poll_id", false) && ok && (user("user") != voterChat) && (!voterChat || publicBotAPIChat(voter))
		for _, value := range ids {
			n, ok := value.(json.Number)
			id, err := n.Int64()
			if !ok || err != nil || id < 0 {
				valid = false
			}
		}
		persistent, supplied := body["option_persistent_ids"].([]any)
		valid = valid && supplied && len(persistent) == len(ids)
		for _, value := range persistent {
			id, ok := value.(string)
			if !ok || id == "" {
				valid = false
			}
		}
	case "chat_boost":
		_, boost := body["boost"].(map[string]any)
		valid = chat() && boost
	case "removed_chat_boost":
		_, source := body["source"].(map[string]any)
		valid = chat() && text("boost_id", false) && number("remove_date", true) && source
	case "managed_bot":
		valid = user("user") && user("bot")
	case "subscription":
		valid = user("user") && text("invoice_payload", true) && (body["state"] == "canceled" || body["state"] == "active" || body["state"] == "failed")
	case "stopped_message_generation":
		valid = chat() && number("draft_id", false)
	}
	if !valid {
		return nil, invalid
	}
	return body, nil
}

func publicBotAPIUser(body map[string]any) bool {
	id, ok := body["id"].(json.Number)
	if !ok {
		return false
	}
	n, err := id.Int64()
	_, bot := body["is_bot"].(bool)
	name, named := body["first_name"].(string)
	if err != nil || n <= 0 || !bot || !named || name == "" {
		return false
	}
	for key := range body {
		switch key {
		case "id", "is_bot", "first_name", "last_name", "username", "language_code", "is_premium", "added_to_attachment_menu", "can_join_groups", "can_read_all_group_messages", "supports_inline_queries", "can_connect_to_business", "has_main_web_app", "has_topics_enabled", "allows_users_to_create_topics":
		default:
			return false
		}
	}
	return true
}

func publicBotAPIChat(body map[string]any) bool {
	id, ok := body["id"].(json.Number)
	if !ok {
		return false
	}
	n, err := id.Int64()
	typ, ok := body["type"].(string)
	if err != nil || !ok || n == 0 {
		return false
	}
	if typ == "private" {
		return n > 0
	}
	return n < 0 && (typ == "group" || typ == "supergroup" || typ == "channel")
}

func publicBotAPIJSON(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if _, user := value["is_bot"]; user && !publicBotAPIUser(value) {
			return false
		}
		for key, child := range value {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if strings.HasPrefix(normalized, "session") || strings.HasPrefix(normalized, "clientsession") || strings.HasPrefix(normalized, "authkey") {
				return false
			}
			switch normalized {
			case "session", "clientsession", "clientsessionmetadata", "authkey", "authkeyid", "accesshash", "randomid", "origin", "origindevice", "payloadhash", "password", "bottoken", "token", "phone", "phonehash":
				return false
			}
			if key != strings.ToLower(key) || !publicBotAPIJSON(child) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !publicBotAPIJSON(child) {
				return false
			}
		}
	}
	return true
}

func ValidateBotAPIObjectRequest(req EnqueueBotAPIUpdateRequest) error {
	if req.Object == nil {
		return nil
	}
	if req.BotUserID <= 0 || req.Object == nil || req.Kind != req.Object.Kind || req.Object.Validate() != nil || req.Date <= 0 || req.Peer != (Peer{}) || req.MessageID != 0 || req.SourcePts != 0 || req.Callback != nil || req.PreCheckout != nil || req.Ephemeral != nil || req.ChatMember != nil || req.Reaction != nil || req.JoinRequest != nil {
		return errors.New("invalid bot api object request")
	}
	return nil
}

func botAPIObjectFields(kind BotAPIUpdateKind) string {
	switch kind {
	case "message", "edited_message", "channel_post", "edited_channel_post", "business_message", "edited_business_message", "guest_message":
		return "message_id message_thread_id direct_messages_topic from sender_chat sender_boost_count sender_business_bot sender_tag date business_connection_id chat forward_origin is_topic_message is_automatic_forward reply_to_message external_reply quote reply_to_story reply_to_checklist_task_id via_bot edit_date has_protected_content is_from_offline is_paid_post media_group_id author_signature paid_star_count text entities link_preview_options suggested_post_info effect_id animation audio document live_photo paid_media photo sticker story video video_note voice caption caption_entities show_caption_above_media has_media_spoiler checklist contact dice game poll venue location new_chat_members left_chat_member new_chat_title new_chat_photo delete_chat_photo group_chat_created supergroup_chat_created channel_chat_created message_auto_delete_timer_changed migrate_to_chat_id migrate_from_chat_id pinned_message invoice successful_payment refunded_payment users_shared chat_shared gift unique_gift gift_upgrade_sent connected_website write_access_allowed passport_data proximity_alert_triggered boost_added chat_background_set checklist_tasks_done checklist_tasks_added direct_message_price_changed forum_topic_created forum_topic_edited forum_topic_closed forum_topic_reopened general_forum_topic_hidden general_forum_topic_unhidden giveaway_created giveaway giveaway_winners giveaway_completed paid_message_price_changed suggested_post_approved suggested_post_approval_failed suggested_post_declined suggested_post_paid suggested_post_refunded video_chat_scheduled video_chat_started video_chat_ended video_chat_participants_invited web_app_data reply_markup guest_query_id guest_bot_caller_user guest_bot_caller_chat managed_bot_created rich_message"
	case "inline_query":
		return "id from query offset chat_type location"
	case "chosen_inline_result":
		return "result_id from query location inline_message_id"
	case "business_connection":
		return "id user user_chat_id date rights is_enabled"
	case "deleted_business_messages":
		return "business_connection_id chat message_ids"
	case "chat_join_request":
		return "chat from user_chat_id date bio invite_link query_id"
	case "shipping_query":
		return "id from invoice_payload shipping_address"
	case "purchased_paid_media":
		return "from paid_media_payload"
	case "poll":
		return "id question question_entities options total_voter_count is_closed is_anonymous type allows_multiple_answers allows_revoting members_only country_codes correct_option_ids explanation explanation_entities explanation_media open_period close_date description description_entities media"
	case "poll_answer":
		return "poll_id voter_chat user option_ids option_persistent_ids"
	case "chat_boost":
		return "chat boost"
	case "removed_chat_boost":
		return "chat boost_id remove_date source"
	case "managed_bot":
		return "user bot"
	case "subscription":
		return "user invoice_payload state"
	case "stopped_message_generation":
		return "chat message_thread_id draft_id"
	}
	return ""
}
