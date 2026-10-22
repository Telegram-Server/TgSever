package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func requestedButtonPeerType(button domain.BotRequestedWebViewButton) tg.RequestPeerTypeClass {
	if button.PeerType == "managed_bot" {
		peer := &tg.RequestPeerTypeCreateBot{BotManaged: true}
		if button.SuggestedName != "" {
			peer.SetSuggestedName(button.SuggestedName)
		}
		if button.SuggestedUsername != "" {
			peer.SetSuggestedUsername(button.SuggestedUsername)
		}
		return peer
	}
	return tgRequestPeerTypeWithFilter(button.PeerType, button.PeerFilter)
}

func fillRequestedManagedButton(out *domain.BotRequestedWebViewButton, peer tg.RequestPeerTypeClass) {
	if created, ok := peer.(*tg.RequestPeerTypeCreateBot); ok && created.BotManaged {
		out.PeerType = "managed_bot"
		out.SuggestedName = created.SuggestedName
		out.SuggestedUsername = created.SuggestedUsername
		out.MaxQuantity = 1
	}
}

func (r *Router) requestedManagedBotMatches(ctx context.Context, userID, managerID, botID int64) (domain.User, bool, error) {
	service, ok := r.deps.Bots.(interface {
		ManagedBot(context.Context, int64, int64) (domain.BotManagement, domain.BotProfile, error)
	})
	if !ok {
		return domain.User{}, false, botInvalidErr()
	}
	_, profile, err := service.ManagedBot(ctx, managerID, botID)
	if err != nil {
		return domain.User{}, false, nil
	}
	if profile.OwnerUserID != userID {
		return domain.User{}, false, nil
	}
	user, found, err := r.deps.Users.ByID(ctx, userID, botID)
	if err != nil || !found || !user.Bot {
		return domain.User{}, false, err
	}
	return domain.User{ID: user.ID, Bot: true, FirstName: user.FirstName, LastName: user.LastName, Username: user.Username}, true, nil
}
