package rpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"
	"strings"
	accountapp "telesrv/internal/app/account"
	botsapp "telesrv/internal/app/bots"
	messagesapp "telesrv/internal/app/messages"
	usersapp "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

type gamesPassportFixture struct {
	r          *Router
	gp         *memory.GamesPassportStore
	messages   *memory.MessageStore
	users      *memory.UserStore
	passwords  *memory.PasswordStore
	owner, bot domain.User
}

func newGamesPassportFixture(t *testing.T) gamesPassportFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	gp := memory.NewGamesPassportStore()
	messages := memory.NewMessageStore()
	messages.BindGamesPassportStore(gp)
	channels := memory.NewChannelStore()
	bots := botsapp.NewService(users, botStore, messages, botsapp.WithGamesPassportStore(gp))
	owner, err := users.Create(ctx, domain.User{AccessHash: 8001, Phone: "15550008801", FirstName: "Player"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Games", "games_passport_bot")
	if err != nil {
		t.Fatal(err)
	}
	passwords := memory.NewPasswordStore()
	commerce := memory.NewBotCommerceStore(memory.NewStarsStore(), botStore, messages, channels)
	router := New(Config{DC: 2}, Deps{Users: usersapp.NewService(users), Bots: bots, Messages: messagesapp.NewService(messages, memory.NewDialogStore()), GamesPassport: gp, Account: accountapp.NewService(passwords), BotBroadcast: commerce}, zaptest.NewLogger(t), clock.System)
	return gamesPassportFixture{r: router, gp: gp, messages: messages, users: users, passwords: passwords, owner: owner, bot: bot}
}
func TestGameMessageScoreAndCallbackOwnership(t *testing.T) {
	f := newGamesPassportFixture(t)
	ctx := context.Background()
	game := domain.BotGame{ID: 801, AccessHash: 802, BotUserID: f.bot.ID, ShortName: "race", Title: "Race", Description: "Finish first", Photo: domain.Photo{ID: 803, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 640, H: 480, Size: 100}}}}
	if err := f.gp.CreateBotGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	sent, err := f.r.BotAPISendGame(ctx, f.bot.ID, domain.BotAPISendGameInput{ChatID: f.owner.ID, ShortName: "race"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, found, err := f.messages.GetByUID(ctx, f.owner.ID, sent.UID)
	if err != nil || !found || recipient.Media.Game.ID != game.ID || recipient.ReplyMarkup.Inline[0][0].Type != domain.MarkupButtonGame {
		t.Fatalf("received game=%+v %v", recipient, err)
	}
	query, err := f.r.resolveBotCallbackQuery(ctx, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, recipient.ID, nil, true)
	if err != nil || query.BotUserID != f.bot.ID || query.GameShortName != "race" || len(query.Data) != 0 {
		t.Fatalf("game callback=%+v %v", query, err)
	}
	noEdit, err := f.r.BotAPISetGameScore(ctx, f.bot.ID, f.owner.ID, sent.ID, f.owner.ID, 10, false, true, nil)
	if err != nil || noEdit.Body != "" {
		t.Fatalf("disabled edit=%+v %v", noEdit, err)
	}
	for _, score := range []int{10, 9} {
		if _, err := f.r.BotAPISetGameScore(ctx, f.bot.ID, f.owner.ID, sent.ID, f.owner.ID, score, false, false, nil); !errors.Is(err, domain.ErrGameScoreNotModified) {
			t.Fatalf("score %d=%v", score, err)
		}
	}
	edited, err := f.r.BotAPISetGameScore(ctx, f.bot.ID, f.owner.ID, sent.ID, f.owner.ID, 4, true, false, nil)
	if err != nil || !strings.Contains(edited.Body, "1. Player — 4") || edited.Media.Game.ID != game.ID {
		t.Fatalf("scoreboard=%+v %v", edited, err)
	}
	scores, err := f.r.BotAPIGetGameHighScores(ctx, f.bot.ID, f.owner.ID, sent.ID, f.owner.ID, nil)
	if err != nil || len(scores) != 1 || scores[0].Score != 4 || scores[0].Position != 1 {
		t.Fatalf("high scores=%+v %v", scores, err)
	}
	if _, err := f.r.BotAPISetGameScore(ctx, f.owner.ID, f.owner.ID, sent.ID, f.owner.ID, 99, true, true, nil); err == nil {
		t.Fatal("non-bot set score")
	}
	inlineSend, err := f.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.owner.ID, RecipientUserID: f.bot.ID, RandomID: 8901, Date: 100, ViaBotID: f.bot.ID, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGame, Game: &game}, ReplyMarkup: sent.ReplyMarkup})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := f.r.inputInlineMessageIDForPrivateMessage(f.bot.ID, inlineSend.SenderMessage).(*tg.InputBotInlineMessageID64)
	if !ok {
		t.Fatal("no signed inline id")
	}
	inline := domainInlineMessageID(id)
	if _, err := f.r.BotAPISetGameScore(ctx, f.bot.ID, 0, 0, f.owner.ID, 77, false, true, inline); err != nil {
		t.Fatal(err)
	}
	bad := *inline
	bad.AccessHash++
	if _, err := f.r.BotAPIGetGameHighScores(ctx, f.bot.ID, 0, 0, f.owner.ID, &bad); err == nil {
		t.Fatal("forged inline capability accepted")
	}
	normal, err := f.r.BotAPIGetGameHighScores(ctx, f.bot.ID, f.owner.ID, sent.ID, f.owner.ID, nil)
	if err != nil || normal[0].Score != 4 {
		t.Fatalf("inline changed chat table=%+v %v", normal, err)
	}
}
func TestPassportSubmissionRejectionVisibleToUser(t *testing.T) {
	f := newGamesPassportFixture(t)
	ctx := WithUserID(context.Background(), f.owner.ID)
	secret := &domain.PassportSecret{ID: 123, Algo: "pbkdf2_hmac_sha512_iter100000", Salt: bytes.Repeat([]byte{1}, 40), Secret: bytes.Repeat([]byte{2}, 32)}
	if err := f.passwords.Save(ctx, f.owner.ID, domain.PasswordSettings{HasPassword: true, SecureSettings: secret}); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err = f.gp.SetPassportPublicKey(ctx, f.bot.ID, public); err != nil {
		t.Fatal(err)
	}
	input := tg.InputSecureValue{Type: &tg.SecureValueTypePersonalDetails{}}
	input.SetData(tg.SecureData{Data: bytes.Repeat([]byte{3}, 48), DataHash: bytes.Repeat([]byte{4}, 32), Secret: bytes.Repeat([]byte{5}, 32)})
	value, err := f.r.onAccountSaveSecureValue(ctx, &tg.AccountSaveSecureValueRequest{Value: input, SecureSecretID: 123})
	if err != nil {
		t.Fatal(err)
	}
	rejection := domain.PassportError{Source: "data", Type: "personal_details", FieldName: "birth_date", DataHash: base64.StdEncoding.EncodeToString(input.Data.DataHash), Message: "Correct your birth date"}
	if _, err = f.r.BotAPISetPassportDataErrors(ctx, f.bot.ID, f.owner.ID, []domain.PassportError{rejection}); !errors.Is(err, domain.ErrPassportInvalid) {
		t.Fatalf("bot rejected unsubmitted data: %v", err)
	}
	request := &tg.AccountAcceptAuthorizationRequest{BotID: f.bot.ID, PublicKey: public, Scope: `{"v":1,"d":["pd"]}`, ValueHashes: []tg.SecureValueHash{{Type: input.Type, Hash: value.Hash}}, Credentials: tg.SecureCredentialsEncrypted{Data: bytes.Repeat([]byte{6}, 48), Hash: bytes.Repeat([]byte{7}, 32), Secret: bytes.Repeat([]byte{8}, 256)}}
	if ok, err := f.r.onAccountAcceptAuthorization(ctx, request); err != nil || !ok {
		t.Fatalf("authorize=%v %v", ok, err)
	}
	if _, err = f.r.BotAPISetPassportDataErrors(ctx, f.bot.ID, f.owner.ID, []domain.PassportError{rejection}); err != nil {
		t.Fatal(err)
	}
	form, err := f.r.onAccountGetAuthorizationForm(ctx, &tg.AccountGetAuthorizationFormRequest{BotID: f.bot.ID, Scope: request.Scope, PublicKey: public})
	if err != nil || len(form.Errors) != 1 {
		t.Fatalf("user error form=%+v %v", form, err)
	}
	detail, ok := form.Errors[0].(*tg.SecureValueErrorData)
	if !ok || detail.Field != "birth_date" || detail.Text != rejection.Message {
		t.Fatalf("wrong visible rejection=%+v", form.Errors)
	}
	if _, err = f.r.onAccountAcceptAuthorization(ctx, request); !errors.Is(err, domain.ErrPassportErrorsPending) {
		t.Fatalf("unchanged resubmit=%v", err)
	}
	if _, err = f.r.onAccountSaveSecureValue(ctx, &tg.AccountSaveSecureValueRequest{Value: input, SecureSecretID: 124}); !tgerr.Is(err, "SECURE_SECRET_INVALID") {
		t.Fatalf("wrong secret accepted: %v", err)
	}
}
