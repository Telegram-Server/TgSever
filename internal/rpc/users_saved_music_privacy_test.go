package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appaccount "telesrv/internal/app/account"
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestBotSavedMusicPrivacyRPC(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	create := func(user domain.User) domain.User {
		t.Helper()
		created, err := users.Create(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	owner := create(domain.User{AccessHash: 41, Phone: "15550013901", FirstName: "Owner"})
	contact := create(domain.User{AccessHash: 42, Phone: "15550013902", FirstName: "Contact"})
	bot := create(domain.User{AccessHash: 44, FirstName: "Music Bot", Username: "music_privacy_bot", Bot: true})
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: contact.ID, FirstName: "Contact"}); err != nil {
		t.Fatal(err)
	}
	passwords := memory.NewPasswordStore()
	files := &fakeFiles{}
	router := New(Config{}, Deps{
		Users:   appusers.NewService(users),
		Privacy: appprivacy.NewService(memory.NewPrivacyStore(), contacts),
		Account: appaccount.NewService(passwords, appaccount.WithSavedMusic(passwords)),
		Files:   files,
	}, zaptest.NewLogger(t), clock.System)
	f := struct {
		r                   *Router
		files               *fakeFiles
		owner, contact, bot domain.User
	}{r: router, files: files, owner: owner, contact: contact, bot: bot}
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)
	first := testMusicDocument(57101, 571001, "First song")
	second := testMusicDocument(57102, 571002, "Second song")
	f.files.docs = map[int64]domain.Document{first.ID: first, second.ID: second}
	if _, err := f.r.onAccountSaveMusic(ownerCtx, &tg.AccountSaveMusicRequest{ID: &tg.InputDocument{ID: first.ID, AccessHash: first.AccessHash}}); err != nil {
		t.Fatal(err)
	}
	req := &tg.AccountSaveMusicRequest{ID: &tg.InputDocument{ID: second.ID, AccessHash: second.AccessHash}}
	req.SetAfterID(&tg.InputDocument{ID: first.ID, AccessHash: first.AccessHash})
	if _, err := f.r.onAccountSaveMusic(ownerCtx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.deps.Privacy.SetRules(ctx, f.owner.ID, domain.PrivacyKeySavedMusic, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatal(err)
	}
	t.Run("bot cannot read contact-only music through list or ID lookup", func(t *testing.T) {
		result, err := f.r.onUsersGetSavedMusic(botCtx, &tg.UsersGetSavedMusicRequest{ID: inputUser(f.owner), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		list := result.(*tg.UsersSavedMusic)
		if list.Count != 0 || len(list.Documents) != 0 {
			t.Fatalf("hidden music = %+v", list)
		}
		result, err = f.r.onUsersGetSavedMusicByID(botCtx, &tg.UsersGetSavedMusicByIDRequest{ID: inputUser(f.owner), Documents: []tg.InputDocumentClass{&tg.InputDocument{ID: first.ID, AccessHash: first.AccessHash}}})
		if err != nil {
			t.Fatal(err)
		}
		list = result.(*tg.UsersSavedMusic)
		if list.Count != 0 || len(list.Documents) != 0 {
			t.Fatalf("hidden music by ID = %+v", list)
		}
		full, err := f.r.onUsersGetFullUser(botCtx, inputUser(f.owner))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := full.FullUser.GetSavedMusic(); ok {
			t.Fatal("hidden music leaked through userFull")
		}
	})
	t.Run("contact sees profile songs", func(t *testing.T) {
		result, err := f.r.onUsersGetSavedMusic(WithUserID(ctx, f.contact.ID), &tg.UsersGetSavedMusicRequest{ID: inputUser(f.owner), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		list := result.(*tg.UsersSavedMusic)
		if list.Count != 2 || len(list.Documents) != 2 || documentClassID(list.Documents[0]) != first.ID || documentClassID(list.Documents[1]) != second.ID {
			t.Fatalf("contact music = %+v", list)
		}
	})
	t.Run("explicitly allowed bot reads paginated songs and total", func(t *testing.T) {
		if _, err := f.r.deps.Privacy.SetRules(ctx, f.owner.ID, domain.PrivacyKeySavedMusic, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}); err != nil {
			t.Fatal(err)
		}
		result, err := f.r.onUsersGetSavedMusic(botCtx, &tg.UsersGetSavedMusicRequest{ID: inputUser(f.owner), Offset: 1, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		list := result.(*tg.UsersSavedMusic)
		if list.Count != 2 || len(list.Documents) != 1 || documentClassID(list.Documents[0]) != second.ID {
			t.Fatalf("bot music page = %+v", list)
		}
	})
}
