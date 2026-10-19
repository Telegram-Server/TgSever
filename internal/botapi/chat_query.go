package botapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/domain"
)

// ChatQueryGateway 提供 getChat / getChatMember / getChatAdministrators /
// getChatMemberCount 所需的只读查询，全部以机器人为查看者。可选接口：网关不实现
// 时这些方法回 501 METHOD_NOT_FOUND。
type ChatQueryGateway interface {
	BotAPIGetChatMemberCount(ctx context.Context, botID, chatID int64) (int, error)
	// BotAPIGetChatMember 返回成员记录、其用户资料，以及机器人能否编辑该管理员。
	BotAPIGetChatMember(ctx context.Context, botID, chatID, userID int64) (domain.ChannelMember, domain.User, bool, error)
	// BotAPIGetChatAdministrators 返回群主与全部管理员（含机器人）、他们的用户资料，
	// 以及按 user id 记录的 can_be_edited。
	BotAPIGetChatAdministrators(ctx context.Context, botID, chatID int64) ([]domain.ChannelMember, []domain.User, map[int64]bool, error)
	// BotAPIGetChatUser 返回私聊对方的资料；About / Birthday 已按对方隐私设置对
	// 机器人裁剪。
	BotAPIGetChatUser(ctx context.Context, botID, userID int64) (domain.User, error)
	// BotAPIGetChatChannel 返回机器人视角的超级群/频道。
	BotAPIGetChatChannel(ctx context.Context, botID, chatID int64) (domain.ChannelView, error)
}

// botAPIChannelChatIDMin 以下（-100 前缀）是超级群/频道；本服没有旧式普通群。
const botAPIChannelChatIDMin = -1000000000000

func init() {
	registerMethods(map[string]methodFunc{
		"getChat":               (*handler).getChat,
		"getChatAdministrators": (*handler).getChatAdministrators,
		"getChatMember":         (*handler).getChatMember,
		"getChatMemberCount":    (*handler).getChatMemberCount,
	})
	registerErrorMarkers("USER_NOT_PARTICIPANT")
}

// chatQueryRequest 取出网关与 chat_id；失败时已写好错误响应。
func (h *handler) chatQueryRequest(w http.ResponseWriter, r *http.Request, botID int64) (ChatQueryGateway, map[string]string, int64, bool) {
	gateway, ok := h.gateway.(ChatQueryGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, 0, false
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return nil, nil, 0, false
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, 0, false
	}
	return gateway, values, chatID, true
}

func (h *handler) getChat(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, _, chatID, ok := h.chatQueryRequest(w, r, botID)
	if !ok {
		return
	}
	switch {
	case chatID > 0:
		user, err := gateway.BotAPIGetChatUser(r.Context(), botID, chatID)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
			return
		}
		writeAPIOK(w, apiPrivateChatFullInfo(user, time.Now().Unix()))
	case chatID < botAPIChannelChatIDMin:
		view, err := gateway.BotAPIGetChatChannel(r.Context(), botID, chatID)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
			return
		}
		writeAPIOK(w, apiChannelChatFullInfo(chatID, view))
	default:
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
	}
}

// apiPrivateChatFullInfo 把私聊对方投影成官方 ChatFullInfo。
func apiPrivateChatFullInfo(u domain.User, now int64) map[string]any {
	out := map[string]any{
		"id":                  u.ID,
		"type":                "private",
		"first_name":          apiUserFirstName(u),
		"accent_color_id":     u.Color.Color,
		"max_reaction_count":  domain.DefaultMessageReactionsUniqMax,
		"accepted_gift_types": apiAcceptedGiftTypes(true, true),
	}
	if u.LastName != "" {
		out["last_name"] = u.LastName
	}
	if u.Username != "" {
		out["username"] = u.Username
	}
	if u.About != "" {
		out["bio"] = u.About
	}
	if b := u.Birthday; b.Day > 0 && b.Month > 0 {
		birth := map[string]any{"day": b.Day, "month": b.Month}
		if b.Year > 0 {
			birth["year"] = b.Year
		}
		out["birthdate"] = birth
	}
	apiPeerColorFields(out, u.Color.BackgroundEmojiID, u.ProfileColor.HasColor, u.ProfileColor.Color, u.ProfileColor.BackgroundEmojiID)
	if u.EmojiStatusActiveAt(now) {
		out["emoji_status_custom_emoji_id"] = strconv.FormatInt(u.EmojiStatusDocumentID, 10)
		if u.EmojiStatusUntil > 0 {
			out["emoji_status_expiration_date"] = u.EmojiStatusUntil
		}
	}
	return out
}

// apiChannelChatFullInfo 把超级群/频道投影成官方 ChatFullInfo。广播频道报 channel，
// 群专属字段（默认权限、慢速模式、入群要求）只在超级群上给出。
func apiChannelChatFullInfo(chatID int64, view domain.ChannelView) map[string]any {
	c := view.Channel
	broadcast := c.Broadcast && !c.Megagroup
	out := map[string]any{
		"id":                  chatID,
		"type":                "supergroup",
		"title":               c.Title,
		"accent_color_id":     c.Color.Color,
		"max_reaction_count":  c.ReactionPolicy.UniqueReactionsLimit(),
		"accepted_gift_types": apiAcceptedGiftTypes(broadcast, false),
	}
	if broadcast {
		out["type"] = "channel"
	}
	if c.Username != "" {
		out["username"] = c.Username
	}
	if c.About != "" {
		out["description"] = c.About
	}
	if c.Forum {
		out["is_forum"] = true
	}
	if c.NoForwards {
		out["has_protected_content"] = true
	}
	if c.ParticipantsHidden {
		out["has_hidden_members"] = true
	}
	if c.LinkedChatID > 0 && view.LinkedChatUsable {
		out["linked_chat_id"] = botAPIChannelChatIDMin - c.LinkedChatID
	}
	if c.TTLPeriod > 0 {
		out["message_auto_delete_time"] = c.TTLPeriod
	}
	if c.SendPaidMessagesStars > 0 {
		out["paid_message_star_count"] = c.SendPaidMessagesStars
	}
	if reactions, ok := apiAvailableReactions(c.ReactionPolicy); ok {
		out["available_reactions"] = reactions
	}
	apiPeerColorFields(out, c.Color.BackgroundEmojiID, c.ProfileColor.HasColor, c.ProfileColor.Color, c.ProfileColor.BackgroundEmojiID)
	if c.EmojiStatus.DocumentID != 0 {
		out["emoji_status_custom_emoji_id"] = strconv.FormatInt(c.EmojiStatus.DocumentID, 10)
		if c.EmojiStatus.Until > 0 {
			out["emoji_status_expiration_date"] = c.EmojiStatus.Until
		}
	}
	if !broadcast {
		out["permissions"] = apiChatPermissions(c.DefaultBannedRights)
		if c.SlowmodeSeconds > 0 {
			out["slow_mode_delay"] = c.SlowmodeSeconds
		}
		if c.BoostsUnrestrict > 0 {
			out["unrestrict_boost_count"] = c.BoostsUnrestrict
		}
		if c.JoinToSend {
			out["join_to_send_messages"] = true
		}
		if c.JoinRequest {
			out["join_by_request"] = true
		}
		// 以下两项官方只对管理员给出。
		if self := view.Self; self.Status == domain.ChannelMemberActive &&
			(self.Role == domain.ChannelRoleCreator || self.Role == domain.ChannelRoleAdmin) {
			out["has_visible_history"] = !c.PreHistoryHidden
			if c.AntiSpam {
				out["has_aggressive_anti_spam_enabled"] = true
			}
		}
	}
	return out
}

func apiPeerColorFields(out map[string]any, backgroundEmojiID int64, hasProfileColor bool, profileColor int, profileEmojiID int64) {
	if backgroundEmojiID != 0 {
		out["background_custom_emoji_id"] = strconv.FormatInt(backgroundEmojiID, 10)
	}
	if hasProfileColor {
		out["profile_accent_color_id"] = profileColor
	}
	if profileEmojiID != 0 {
		out["profile_background_custom_emoji_id"] = strconv.FormatInt(profileEmojiID, 10)
	}
}

// apiAvailableReactions 投影 available_reactions；官方省略即"所有普通 emoji 都可用"，
// 所以只有显式白名单或全部禁用时才给出。
func apiAvailableReactions(p domain.ChannelReactionPolicy) ([]map[string]any, bool) {
	switch p.Type {
	case domain.ChannelReactionPolicyNone:
		return []map[string]any{}, true
	case domain.ChannelReactionPolicySome:
		out := make([]map[string]any, 0, len(p.Emoticons)+len(p.CustomEmojiIDs))
		for _, emoji := range p.Emoticons {
			out = append(out, map[string]any{"type": "emoji", "emoji": emoji})
		}
		for _, id := range p.CustomEmojiIDs {
			out = append(out, map[string]any{"type": "custom_emoji", "custom_emoji_id": strconv.FormatInt(id, 10)})
		}
		return out, true
	default:
		return nil, false
	}
}

// apiAcceptedGiftTypes 是必填的 AcceptedGiftTypes。本服未向网关暴露用户的礼物接收
// 开关，用户与频道按官方默认（全部接收）给出，Premium 订阅只能送给用户；超级群不能
// 收礼物，全部为 false。
func apiAcceptedGiftTypes(gifts, premium bool) map[string]bool {
	return map[string]bool{
		"unlimited_gifts":      gifts,
		"limited_gifts":        gifts,
		"unique_gifts":         gifts,
		"premium_subscription": premium,
		"gifts_from_channels":  gifts,
	}
}

func (h *handler) getChatMember(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatQueryRequest(w, r, botID)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(values["user_id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
		return
	}
	member, user, canBeEdited, err := gateway.BotAPIGetChatMember(r.Context(), botID, chatID, userID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, apiChatMember(member, user, canBeEdited))
}

// getChatAdministrators 默认省略当前机器人以外的其他机器人；return_bots=true 时全部返回。
func (h *handler) getChatAdministrators(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatQueryRequest(w, r, botID)
	if !ok {
		return
	}
	returnBots := strings.EqualFold(strings.TrimSpace(values["return_bots"]), "true")
	members, users, editable, err := gateway.BotAPIGetChatAdministrators(r.Context(), botID, chatID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	userByID := make(map[int64]domain.User, len(users))
	for _, u := range users {
		userByID[u.ID] = u
	}
	out := make([]map[string]any, 0, len(members))
	for _, member := range members {
		user := userByID[member.UserID]
		if user.Bot && member.UserID != botID && !returnBots {
			continue
		}
		out = append(out, apiChatMember(member, user, editable[member.UserID]))
	}
	writeAPIOK(w, out)
}

func (h *handler) getChatMemberCount(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, _, chatID, ok := h.chatQueryRequest(w, r, botID)
	if !ok {
		return
	}
	count, err := gateway.BotAPIGetChatMemberCount(r.Context(), botID, chatID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, count)
}
