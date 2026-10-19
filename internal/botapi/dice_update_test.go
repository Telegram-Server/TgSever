package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

func TestAPIUpdateProjectsIncomingDice(t *testing.T) {
	item, kind, ok := apiUpdate(domain.UpdateEvent{
		Type: domain.UpdateEventNewMessage,
		Pts:  13,
		Message: domain.Message{
			ID:          5,
			OwnerUserID: 1001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 2001},
			Date:        1700000007,
			Media: &domain.MessageMedia{
				Kind: domain.MessageMediaKindDice,
				Dice: &domain.MessageDice{Emoticon: "🎲", Value: 4},
			},
		},
	})
	if !ok || kind != "message" {
		t.Fatalf("apiUpdate ok=%v kind=%q, want a message update for a bare dice", ok, kind)
	}
	msg, _ := item["message"].(map[string]any)
	dice, _ := msg["dice"].(map[string]any)
	if dice["emoji"] != "🎲" || dice["value"] != 4 {
		t.Fatalf("message = %#v, want dice 🎲 4", msg)
	}
}
