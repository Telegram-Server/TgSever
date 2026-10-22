package storetest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

func BotGamesPassport(t *testing.T, gp store.BotGamesPassportStore, messages store.MessageStore, owner, bot, otherBot int64) {
	t.Helper()
	ctx := context.Background()
	game := domain.BotGame{ID: 9911, AccessHash: 9912, BotUserID: bot, ShortName: "race", Title: "Race", Description: "Finish first", Photo: domain.Photo{ID: 7711}}
	if err := gp.CreateBotGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	scope := domain.GameScoreScope{GameID: game.ID, ChatID: owner}
	if err := gp.SetBotGameScore(ctx, scope, owner, 40, false); err != nil {
		t.Fatal(err)
	}
	for _, score := range []int{40, 39} {
		if err := gp.SetBotGameScore(ctx, scope, owner, score, false); !errors.Is(err, domain.ErrGameScoreNotModified) {
			t.Fatalf("non-increase %d: %v", score, err)
		}
	}
	if err := gp.SetBotGameScore(ctx, scope, owner, 7, true); err != nil {
		t.Fatal(err)
	}
	scores, err := gp.BotGameScores(ctx, scope)
	if err != nil || len(scores) != 1 || scores[0].Score != 7 || scores[0].UserID != owner {
		t.Fatalf("forced leaderboard=%+v err=%v", scores, err)
	}
	otherScope := scope
	otherScope.InlineOwnerID = owner
	otherScope.InlineMessageID = 99
	if err := gp.SetBotGameScore(ctx, otherScope, owner, 88, false); err != nil {
		t.Fatal(err)
	}
	scores, err = gp.BotGameScores(ctx, scope)
	if err != nil || scores[0].Score != 7 {
		t.Fatalf("inline leaked into normal scores: %+v %v", scores, err)
	}
	hash := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	value := tg.SecureValue{Type: &tg.SecureValueTypePersonalDetails{}, Hash: bytes.Repeat([]byte{2}, 32)}
	value.SetData(tg.SecureData{Data: bytes.Repeat([]byte{3}, 48), DataHash: bytes.Repeat([]byte{1}, 32), Secret: bytes.Repeat([]byte{4}, 32)})
	var wire bin.Buffer
	if err := value.Encode(&wire); err != nil {
		t.Fatal(err)
	}
	element := domain.PassportElement{Type: "personal_details", SecretID: 77, Hash: base64.StdEncoding.EncodeToString(value.Hash), DataHash: hash, Data: base64.StdEncoding.EncodeToString(value.Data.Data), ValueTL: wire.Copy()}
	if err := gp.SavePassportElement(ctx, owner, element); err != nil {
		t.Fatal(err)
	}
	rejection := domain.PassportError{Source: "data", Type: element.Type, FieldName: "birth_date", DataHash: hash, Message: "Check the birth date"}
	if err := gp.SetPassportErrors(ctx, owner, bot, []domain.PassportError{rejection}); !errors.Is(err, domain.ErrPassportInvalid) {
		t.Fatalf("unreceived data can be rejected: %v", err)
	}
	authorization := domain.PassportAuthorization{BotUserID: bot, Elements: []domain.PassportElement{element}, CredentialsData: bytes.Repeat([]byte{5}, 48), CredentialsHash: bytes.Repeat([]byte{6}, 32), CredentialsSecret: bytes.Repeat([]byte{7}, 256)}
	req := domain.SendPrivateTextRequest{SenderUserID: owner, RecipientUserID: bot, Date: 100, RandomID: 7001, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionPassportData, PassportData: &authorization}}}
	req.ReplyTo = &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bot}, MessageID: 9999}
	if _, err := messages.SendPrivateText(ctx, req); err == nil {
		t.Fatal("invalid reply accepted")
	}
	data, err := gp.GetPassportData(ctx, owner)
	if err != nil || len(data.Authorizations) != 0 {
		t.Fatalf("failed send left a grant: %+v %v", data.Authorizations, err)
	}
	req.ReplyTo = nil
	req.RandomID++
	sent, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if sent.RecipientMessage.OwnerUserID != bot || sent.RecipientMessage.Media.ServiceAction.PassportData.Elements[0].Hash != element.Hash {
		t.Fatal("bot did not receive the authorized encrypted element")
	}
	if err := gp.SetPassportErrors(ctx, owner, otherBot, []domain.PassportError{rejection}); !errors.Is(err, domain.ErrPassportInvalid) {
		t.Fatalf("other bot may reject data: %v", err)
	}
	wrong := rejection
	wrong.DataHash = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	if err := gp.SetPassportErrors(ctx, owner, bot, []domain.PassportError{wrong}); !errors.Is(err, domain.ErrPassportInvalid) {
		t.Fatalf("wrong hash accepted: %v", err)
	}
	if err := gp.SetPassportErrors(ctx, owner, bot, []domain.PassportError{rejection}); err != nil {
		t.Fatal(err)
	}
	req.RandomID++
	if _, err := messages.SendPrivateText(ctx, req); !errors.Is(err, domain.ErrPassportErrorsPending) {
		t.Fatalf("unchanged rejected form resubmitted: %v", err)
	}
	data, err = gp.GetPassportData(ctx, owner)
	if err != nil || data.Authorizations[0].Errors[0].Message != rejection.Message {
		t.Fatalf("user-visible errors missing: %+v %v", data, err)
	}
	value.Hash = bytes.Repeat([]byte{10}, 32)
	value.Data.DataHash = bytes.Repeat([]byte{11}, 32)
	value.Data.Data = bytes.Repeat([]byte{12}, 48)
	wire.Buf = wire.Buf[:0]
	if err := value.Encode(&wire); err != nil {
		t.Fatal(err)
	}
	element.Hash = base64.StdEncoding.EncodeToString(value.Hash)
	element.DataHash = base64.StdEncoding.EncodeToString(value.Data.DataHash)
	element.Data = base64.StdEncoding.EncodeToString(value.Data.Data)
	element.ValueTL = wire.Copy()
	if err := gp.SavePassportElement(ctx, owner, element); err != nil {
		t.Fatal(err)
	}
	authorization.Elements = []domain.PassportElement{element}
	req.RandomID++
	if _, err := messages.SendPrivateText(ctx, req); err != nil {
		t.Fatalf("corrected form: %v", err)
	}
	data, err = gp.GetPassportData(ctx, owner)
	if err != nil || len(data.Authorizations[0].Errors) != 0 {
		t.Fatalf("correction retained obsolete errors: %+v %v", data, err)
	}
}
