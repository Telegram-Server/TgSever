package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func (r *Router) businessConnection(ctx context.Context, botID int64, connectionID string) (domain.ConnectedBusinessBot, error) {
	bot, err := r.BotAPISelf(ctx, botID)
	if err != nil {
		return domain.ConnectedBusinessBot{}, err
	}
	if bot.Deleted {
		return domain.ConnectedBusinessBot{}, errors.New("BOT_INVALID")
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return domain.ConnectedBusinessBot{}, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	connection, found, err := svc.GetBusinessBotConnection(ctx, connectionID)
	if err != nil {
		return domain.ConnectedBusinessBot{}, err
	}
	if !found || connection.BotUserID != botID || connectionID == "" {
		return domain.ConnectedBusinessBot{}, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	owner, found, err := r.deps.Users.ByID(ctx, botID, connection.OwnerUserID)
	if err != nil {
		return domain.ConnectedBusinessBot{}, err
	}
	if !found || owner.Deleted || owner.Bot {
		return domain.ConnectedBusinessBot{}, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	return connection, nil
}

func (r *Router) botAPIBusinessOwner(ctx context.Context, botID int64, connectionID string, right string) (int64, error) {
	connection, err := r.businessConnection(ctx, botID, connectionID)
	if err != nil {
		return 0, err
	}
	if connection.Disabled {
		return 0, errors.New("BUSINESS_CONNECTION_DISABLED")
	}
	if !connection.Rights.Allows(right) {
		return 0, errors.New("BUSINESS_BOT_RIGHT_REQUIRED")
	}
	return connection.OwnerUserID, nil
}

func (r *Router) botAPIBusinessPeer(ctx context.Context, botID int64, connectionID string, right string, peer domain.Peer) (int64, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, right)
	if err != nil {
		return 0, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID <= 0 || peer.ID == ownerID || peer.ID == botID {
		return 0, errors.New("CHAT_ID_INVALID")
	}
	svc, _ := r.accountBusinessAutomation()
	connection, found, err := svc.GetConnectedBusinessBot(ctx, ownerID)
	if err != nil {
		return 0, err
	}
	if !found || connection.ConnectionID != connectionID || connection.Disabled {
		return 0, errors.New("BUSINESS_CONNECTION_DISABLED")
	}
	state, _, err := svc.GetConnectedBusinessBotPeerState(ctx, ownerID, peer.ID)
	if err != nil {
		return 0, err
	}
	if state.Disabled || state.Paused {
		return 0, errors.New("BUSINESS_PEER_DISABLED")
	}
	existing, contact := false, false
	if connection.Recipients.ExistingChats != connection.Recipients.NewChats {
		if r.deps.Messages == nil || connection.CreatedAtUnix <= 0 {
			return 0, errors.New("INTERNAL")
		}
		before, err := r.deps.Messages.Search(ctx, ownerID, domain.MessageFilter{HasPeer: true, Peer: peer, MaxDate: int(connection.CreatedAtUnix) - 1, Limit: 1})
		if err != nil {
			return 0, err
		}
		existing = len(before.Messages) > 0
	}
	if connection.Recipients.Contacts != connection.Recipients.NonContacts {
		if r.deps.Contacts == nil {
			return 0, errors.New("INTERNAL")
		}
		settings, err := r.deps.Contacts.GetPeerSettings(ctx, ownerID, peer)
		if err != nil {
			return 0, err
		}
		contact = !settings.AddContact
	}
	if !domain.BusinessBotRecipientsMatch(connection.Recipients, existing, contact, peer.ID) {
		return 0, errors.New("BUSINESS_PEER_INVALID")
	}
	if right == "can_reply" || right == "can_read_messages" {
		if r.deps.Messages == nil {
			return 0, errors.New("INTERNAL")
		}
		senderID := int64(0)
		if right == "can_reply" {
			senderID = peer.ID
		}
		recent, err := r.deps.Messages.Search(ctx, ownerID, domain.MessageFilter{HasPeer: true, Peer: peer, SenderUserID: senderID, MinDate: int(r.clock.Now().Unix()) - 24*60*60, Limit: 1})
		if err != nil {
			return 0, err
		}
		if len(recent.Messages) == 0 {
			return 0, errors.New("BUSINESS_CHAT_NOT_ACTIVE")
		}
	}
	return ownerID, nil
}

func (r *Router) BotAPIGetBusinessConnection(ctx context.Context, botID int64, connectionID string) (domain.ConnectedBusinessBot, domain.User, error) {
	connection, err := r.businessConnection(ctx, botID, connectionID)
	if err != nil {
		return domain.ConnectedBusinessBot{}, domain.User{}, err
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, connection.OwnerUserID)
	if err != nil {
		return domain.ConnectedBusinessBot{}, domain.User{}, err
	}
	if !found || user.Deleted {
		return domain.ConnectedBusinessBot{}, domain.User{}, errors.New("USER_ID_INVALID")
	}
	return connection, user, nil
}

func (r *Router) onAccountGetBotBusinessConnection(ctx context.Context, connectionID string) (tg.UpdatesClass, error) {
	botID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	connection, user, err := r.BotAPIGetBusinessConnection(ctx, botID, connectionID)
	if err != nil {
		return nil, tgerr400("CONNECTION_ID_INVALID")
	}
	out := tg.BotBusinessConnection{ConnectionID: connection.ConnectionID, UserID: connection.OwnerUserID, Disabled: connection.Disabled, DCID: r.cfg.DC, Date: int(connection.CreatedAtUnix)}
	out.SetRights(tgBusinessBotRights(connection.Rights))
	return &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateBotBusinessConnect{Connection: out}}, Users: []tg.UserClass{r.tgUser(user)}, Chats: []tg.ChatClass{}, Date: int(r.clock.Now().Unix())}, nil
}

func (r *Router) enqueueBusinessConnection(ctx context.Context, connection domain.ConnectedBusinessBot) {
	if connection.ConnectionID == "" || r.deps.Users == nil {
		return
	}
	user, found, err := r.deps.Users.ByID(ctx, connection.BotUserID, connection.OwnerUserID)
	if err != nil || !found {
		return
	}
	r.enqueueBotAPIObject(ctx, connection.BotUserID, domain.BotAPIUpdateKind("business_connection"), botapi.BusinessConnectionObject(connection, user))
}

func (r *Router) botAPIEnqueueBusinessMessage(ctx context.Context, botID int64, connectionID string, msg domain.Message, edited bool) {
	if msg.ID <= 0 || msg.Deleted || r.deps.Users == nil {
		return
	}
	var users []domain.User
	for _, id := range []int64{msg.OwnerUserID, msg.From.ID, msg.Peer.ID} {
		if id <= 0 {
			continue
		}
		if user, found, err := r.deps.Users.ByID(ctx, botID, id); err == nil && found {
			users = append(users, user)
		}
	}
	object := botapi.SnapshotMessage(msg, users, nil)
	object["business_connection_id"] = connectionID
	if msg.BusinessBotID != 0 {
		if sender, found, err := r.deps.Users.ByID(ctx, botID, msg.BusinessBotID); err == nil && found {
			object["sender_business_bot"] = map[string]any{"id": sender.ID, "is_bot": true, "first_name": sender.FirstName, "username": sender.Username}
		}
	}
	kind := domain.BotAPIUpdateKind("business_message")
	if edited {
		kind = domain.BotAPIUpdateKind("edited_business_message")
	}
	r.enqueueBotAPIObject(ctx, botID, kind, object)
}

func (r *Router) BotAPISetBusinessAccountName(ctx context.Context, botID int64, connectionID, firstName, lastName string) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_edit_name")
	if err != nil {
		return false, err
	}
	if utf8.RuneCountInString(firstName) < 1 || utf8.RuneCountInString(firstName) > 64 || utf8.RuneCountInString(lastName) > 64 {
		return false, errors.New("FIRSTNAME_INVALID")
	}
	svc, ok := r.deps.Users.(interface {
		UpdateBusinessProfile(context.Context, int64, domain.UserProfileUpdate) (domain.User, error)
	})
	if !ok {
		return false, errors.New("INTERNAL")
	}
	user, err := svc.UpdateBusinessProfile(ctx, ownerID, domain.UserProfileUpdate{FirstName: firstName, LastName: lastName, HasFirstName: true, HasLastName: true})
	if err != nil {
		return false, err
	}
	r.invalidateRPCProjectionForUser(user.ID)
	r.pushUsernameUpdate(ctx, user)
	return true, nil
}

func (r *Router) BotAPISetBusinessAccountUsername(ctx context.Context, botID int64, connectionID, username string) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_edit_username")
	if err != nil {
		return false, err
	}
	if utf8.RuneCountInString(username) > 32 {
		return false, errors.New("USERNAME_INVALID")
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return false, errors.New("INTERNAL")
	}
	user, err := svc.UpdateUsername(ctx, ownerID, username)
	if err != nil {
		return false, err
	}
	r.invalidateRPCProjectionForUser(user.ID)
	r.pushUsernameUpdate(ctx, user)
	return true, nil
}

func (r *Router) BotAPISetBusinessAccountBio(ctx context.Context, botID int64, connectionID, bio string) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_edit_bio")
	if err != nil {
		return false, err
	}
	if utf8.RuneCountInString(bio) > 140 {
		return false, errors.New("ABOUT_TOO_LONG")
	}
	svc, ok := r.deps.Users.(interface {
		UpdateBusinessProfile(context.Context, int64, domain.UserProfileUpdate) (domain.User, error)
	})
	if !ok {
		return false, errors.New("INTERNAL")
	}
	user, err := svc.UpdateBusinessProfile(ctx, ownerID, domain.UserProfileUpdate{About: bio, HasAbout: true})
	if err != nil {
		return false, err
	}
	r.invalidateRPCProjectionForUser(user.ID)
	r.pushSelfUserChangedUpdate(ctx, user)
	return true, nil
}

func (r *Router) BotAPISetBusinessAccountProfilePhoto(ctx context.Context, botID int64, connectionID string, data []byte, animated bool, timestamp float64, public bool) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_edit_profile_photo")
	if err != nil {
		return false, err
	}
	if r.deps.Files == nil {
		return false, errors.New("PHOTO_INVALID")
	}
	var photo domain.Photo
	if animated {
		photo, err = r.deps.Files.CreateAvatarVideoFromBytes(ctx, data, timestamp)
	} else {
		photo, err = r.deps.Files.CreateAvatarFromBytes(ctx, data)
	}
	if err != nil {
		return false, err
	}
	kind := domain.ProfilePhotoKindProfile
	if public {
		kind = domain.ProfilePhotoKindFallback
	}
	photo, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, kind, photo.ID, int(r.clock.Now().Unix()))
	if err != nil {
		return false, err
	}
	if !found {
		return false, errors.New("PHOTO_INVALID")
	}
	r.photosPhotoForSelf(ctx, ownerID, photo, kind)
	return true, nil
}

func (r *Router) BotAPIRemoveBusinessAccountProfilePhoto(ctx context.Context, botID int64, connectionID string, public bool) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_edit_profile_photo")
	if err != nil {
		return false, err
	}
	if r.deps.Files == nil {
		return false, errors.New("PHOTO_INVALID")
	}
	kind := domain.ProfilePhotoKindProfile
	if public {
		kind = domain.ProfilePhotoKindFallback
	}
	if public {
		// Public photos have no restorable history: removing one must not
		// expose an older fallback that was superseded by a later upload.
		for {
			photos, _, err := r.deps.Files.GetProfilePhotosKind(ctx, domain.PeerTypeUser, ownerID, kind, 0, 100, 0)
			if err != nil {
				return false, err
			}
			if len(photos) == 0 {
				break
			}
			ids := make([]int64, len(photos))
			for i, photo := range photos {
				ids[i] = photo.ID
			}
			deleted, err := r.deps.Files.DeleteProfilePhotosKind(ctx, domain.PeerTypeUser, ownerID, kind, ids)
			if err != nil {
				return false, err
			}
			if deleted == 0 {
				return false, errors.New("PHOTO_INVALID")
			}
		}
	} else {
		current, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, kind)
		if err != nil {
			return false, err
		}
		if found {
			if _, err := r.deps.Files.DeleteProfilePhotosKind(ctx, domain.PeerTypeUser, ownerID, kind, []int64{current.ID}); err != nil {
				return false, err
			}
		}
	}
	next, _, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, kind)
	if err != nil {
		return false, err
	}
	r.photosPhotoForSelf(ctx, ownerID, next, kind)
	return true, nil
}

func (r *Router) BotAPISetBusinessAccountGiftSettings(ctx context.Context, botID int64, connectionID string, show bool, denied domain.DisallowedGifts) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_change_gift_settings")
	if err != nil {
		return false, err
	}
	settings, ok := r.accountSettingsSvc()
	if !ok {
		return false, errors.New("INTERNAL")
	}
	current, err := settings.GetAccountSettings(ctx, ownerID)
	if err != nil {
		return false, err
	}
	privacy := current.GlobalPrivacy
	privacy.DisplayGiftsButton, privacy.DisallowedGifts = show, denied
	saved, err := settings.SetGlobalPrivacy(ctx, ownerID, privacy)
	if err != nil {
		return false, err
	}
	r.accountSettings.Store(ownerID, saved)
	r.invalidateRPCProjectionForUser(ownerID)
	return true, nil
}

func (r *Router) BotAPIGetBusinessAccountStarBalance(ctx context.Context, botID int64, connectionID string) (domain.StarsBalance, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_view_gifts_and_stars")
	if err != nil {
		return domain.StarsBalance{}, err
	}
	if r.deps.Stars == nil {
		return domain.StarsBalance{}, errors.New("INTERNAL")
	}
	return r.deps.Stars.GetBalance(ctx, ownerID)
}

func (r *Router) BotAPITransferBusinessAccountStars(ctx context.Context, botID int64, connectionID string, count int64) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_transfer_stars")
	if err != nil {
		return false, err
	}
	if count < 1 || count > 10000 {
		return false, errors.New("STAR_COUNT_INVALID")
	}
	if r.deps.BotCommerce == nil {
		return false, errors.New("INTERNAL")
	}
	if err := r.deps.BotCommerce.BusinessTransfer(ctx, ownerID, botID, count, int(r.clock.Now().Unix())); err != nil {
		return false, starsErr(err)
	}
	return true, nil
}

func (r *Router) BotAPIGetBusinessAccountGifts(ctx context.Context, botID int64, connectionID string, filter domain.SavedStarGiftFilter) (domain.BotAPIOwnedGiftPage, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_view_gifts_and_stars")
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, err
	}
	return r.botAPIOwnedGifts(ctx, botID, domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}, filter, true)
}

func (r *Router) BotAPIReadBusinessMessage(ctx context.Context, botID int64, connectionID string, chatID int64, messageID int) (bool, error) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: chatID}
	ownerID, err := r.botAPIBusinessPeer(ctx, botID, connectionID, "can_read_messages", peer)
	if err != nil {
		return false, err
	}
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return false, errors.New("MESSAGE_ID_INVALID")
	}
	message, err := r.botAPIReloadMessage(ctx, ownerID, peer, messageID)
	if err != nil {
		return false, err
	}
	if message.Out || message.From != peer {
		return false, errors.New("MESSAGE_ID_INVALID")
	}
	read, err := r.deps.Messages.ReadHistory(ctx, ownerID, domain.ReadHistoryRequest{OwnerUserID: ownerID, Peer: peer, MaxID: messageID, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return false, err
	}
	if read.Changed {
		r.pushReadHistoryEvent(ctx, ownerID, read.InboxEvent)
	}
	if read.OutboxChanged {
		r.pushReadHistoryEvent(ctx, read.OutboxUserID, read.OutboxEvent)
	}
	return true, nil
}

func (r *Router) BotAPIDeleteBusinessMessages(ctx context.Context, botID int64, connectionID string, ids []int) (bool, error) {
	ownerID, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "")
	if err != nil {
		return false, err
	}
	connection, err := r.businessConnection(ctx, botID, connectionID)
	if err != nil {
		return false, err
	}
	if !connection.Rights.DeleteReceivedMessages && !connection.Rights.DeleteSentMessages {
		return false, errors.New("BUSINESS_BOT_RIGHT_REQUIRED")
	}
	if len(ids) < 1 || len(ids) > 100 || r.deps.Messages == nil {
		return false, errors.New("MESSAGE_ID_INVALID")
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return false, errors.New("MESSAGE_ID_INVALID")
		}
	}
	list, err := r.deps.Messages.GetMessages(ctx, ownerID, ids)
	if err != nil {
		return false, err
	}
	found := make(map[int]domain.Message, len(list.Messages))
	for _, message := range list.Messages {
		if !message.Deleted {
			found[message.ID] = message
		}
	}
	var peer domain.Peer
	now := int(r.clock.Now().Unix())
	unique := make([]int, 0, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		message, ok := found[id]
		if !ok {
			return false, errors.New("MESSAGE_ID_INVALID")
		}
		if peer.ID == 0 {
			peer = message.Peer
		}
		if message.Peer != peer {
			return false, errors.New("MESSAGE_ID_INVALID")
		}
		if !connection.Rights.DeleteReceivedMessages && (message.BusinessConnectionID != connectionID || message.BusinessBotID != botID || !message.Out) {
			return false, errors.New("BUSINESS_BOT_RIGHT_REQUIRED")
		}
		age := now - message.Date
		if age < 0 || age >= 48*60*60 || message.Media != nil && message.Media.Kind == domain.MessageMediaKindDice && age <= 24*60*60 {
			return false, errors.New("MESSAGE_DELETE_FORBIDDEN")
		}
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	if _, err := r.botAPIBusinessPeer(ctx, botID, connectionID, "", peer); err != nil {
		return false, err
	}
	result, err := r.deps.Messages.DeleteMessages(ctx, ownerID, domain.DeleteMessagesRequest{OwnerUserID: ownerID, IDs: unique, Revoke: true, Date: now, Audit: domain.BotDeletionAudit(botID)})
	if err != nil {
		return false, err
	}
	r.notifyPushDeletes(result)
	deleted := result.Self().MessageIDs
	if len(deleted) == 0 {
		return false, errors.New("MESSAGE_ID_INVALID")
	}
	r.enqueueBotAPIObject(ctx, botID, domain.BotAPIUpdateKind("deleted_business_messages"), map[string]any{"business_connection_id": connectionID, "chat": map[string]any{"id": peer.ID, "type": "private"}, "message_ids": deleted})
	return true, nil
}

func (r *Router) enqueueBotAPIBusinessPrivateMessage(ctx context.Context, result domain.SendPrivateTextResult) {
	if result.Duplicate {
		return
	}
	for _, message := range []domain.Message{result.SenderMessage, result.RecipientMessage} {
		r.enqueueConnectedBusinessMessage(ctx, message, false)
	}
}

func (r *Router) enqueueBotAPIBusinessPrivateEdits(ctx context.Context, result domain.EditMessageResult) {
	for _, item := range result.Edited {
		r.enqueueConnectedBusinessMessage(ctx, item.Message, true)
	}
}

func (r *Router) enqueueConnectedBusinessMessage(ctx context.Context, message domain.Message, edited bool) {
	svc, ok := r.accountBusinessAutomation()
	if !ok || message.ID == 0 || message.Peer.Type != domain.PeerTypeUser || message.Deleted {
		return
	}
	connection, found, err := svc.GetConnectedBusinessBot(ctx, message.OwnerUserID)
	if err != nil || !found || connection.Disabled {
		return
	}
	// Explicit business sends/edits already enqueue their owner's result.
	// A recipient with a separate connection still receives its incoming event.
	if connection.ConnectionID == message.BusinessConnectionID {
		return
	}
	if _, err := r.botAPIBusinessPeer(ctx, connection.BotUserID, connection.ConnectionID, "", message.Peer); err != nil {
		return
	}
	r.botAPIEnqueueBusinessMessage(ctx, connection.BotUserID, connection.ConnectionID, message, edited)
}

func (r *Router) enqueueBotAPIBusinessPrivateDeletes(ctx context.Context, result domain.DeleteMessagesResult) {
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return
	}
	for _, item := range result.Deleted {
		connection, found, err := svc.GetConnectedBusinessBot(ctx, item.UserID)
		if err != nil || !found || connection.Disabled {
			continue
		}
		for _, group := range item.ByPeer {
			if len(group.MessageIDs) == 0 {
				continue
			}
			if _, err := r.botAPIBusinessPeer(ctx, connection.BotUserID, connection.ConnectionID, "", group.Peer); err != nil {
				continue
			}
			r.enqueueBotAPIObject(ctx, connection.BotUserID, domain.BotAPIUpdateKind("deleted_business_messages"), map[string]any{"business_connection_id": connection.ConnectionID, "chat": map[string]any{"id": group.Peer.ID, "type": "private"}, "message_ids": group.MessageIDs})
		}
	}
}

func (r *Router) botAPIBusinessGiftAllowed(ctx context.Context, recipientUserID int64, unique, limited, senderIsChannel bool) error {
	settings, err := r.cachedAccountSettings(ctx, recipientUserID)
	if err != nil {
		return err
	}
	denied := settings.GlobalPrivacy.DisallowedGifts
	if senderIsChannel && denied.StargiftsFromChannel || unique && denied.UniqueStargifts || !unique && limited && denied.LimitedStargifts || !unique && !limited && denied.UnlimitedStargifts {
		return tgerr400("USER_PRIVACY_RESTRICTED")
	}
	return nil
}
