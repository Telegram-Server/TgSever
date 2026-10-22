package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	appaccount "telesrv/internal/app/account"
	appbots "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appgifts "telesrv/internal/app/stargifts"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/postgres"
)

const ownedGiftsNow = 1_800_000_000

type ownedGiftFixture struct {
	ctx                           context.Context
	r                             *Router
	h                             http.Handler
	users                         store.UserStore
	messages                      store.MessageStore
	gifts                         store.StarGiftStore
	stars                         store.StarsStore
	account                       *appaccount.Service
	channels                      *appchannels.Service
	owner, sender, recipient, bot domain.User
	connection                    string
	docID                         int64
}

type ownedGiftHTTPPage struct {
	Count int              `json:"total_count"`
	Gifts []map[string]any `json:"gifts"`
	Next  string           `json:"next_offset"`
}

func newOwnedGiftFixture(t *testing.T, backend string) *ownedGiftFixture {
	t.Helper()
	f := &ownedGiftFixture{ctx: context.Background(), docID: time.Now().UnixNano() & 0x7ffffffffffff000}
	var botStore store.BotStore
	var giftsOptions []appgifts.Option
	if backend == "postgres" {
		pool := testPostgresPool(t)
		users, messages, gifts, stars, passwords := postgres.NewUserStore(pool), postgres.NewMessageStore(pool), postgres.NewStarGiftStore(pool), postgres.NewStarsStore(pool), postgres.NewPasswordStore(pool)
		f.users, f.messages, f.gifts, f.stars = users, messages, gifts, stars
		botStore = postgres.NewBotStore(pool)
		f.account = appaccount.NewService(passwords, appaccount.WithBusinessAutomation(passwords), appaccount.WithAccountSettings(passwords))
		f.channels = appchannels.NewService(postgres.NewChannelStore(pool))
		giftsOptions = []appgifts.Option{appgifts.WithUpgradeStore(postgres.NewStarGiftUpgradeStore(pool, messages)), appgifts.WithLifecycleStore(postgres.NewStarGiftLifecycleStore(pool, messages, 0))}
	} else {
		users, messages, gifts, stars, passwords := memory.NewUserStore(), memory.NewMessageStore(), memory.NewStarGiftStore(), memory.NewStarsStore(), memory.NewPasswordStore()
		f.users, f.messages, f.gifts, f.stars = users, messages, gifts, stars
		botStore = memory.NewBotStore(users)
		f.account = appaccount.NewService(passwords, appaccount.WithBusinessAutomation(passwords), appaccount.WithAccountSettings(passwords))
		f.channels = appchannels.NewService(memory.NewChannelStore())
		giftsOptions = []appgifts.Option{appgifts.WithOwnerStore(memory.NewStarGiftOwnerStore(gifts, stars, messages, domain.StarGiftLifecyclePolicy{TransferStars: 25, DropOriginalDetailsStars: 25}))}
	}
	suffix := time.Now().UnixNano() % 1000000000
	for i, target := range []*domain.User{&f.owner, &f.sender, &f.recipient} {
		u, err := f.users.Create(f.ctx, domain.User{Phone: fmt.Sprintf("+1719%09d%d", suffix, i), FirstName: fmt.Sprintf("Gift %d", i), AccessHash: suffix + int64(i) + 1})
		if err != nil {
			t.Fatal(err)
		}
		*target = u
	}
	if err := f.users.UpdateLastSeen(f.ctx, f.recipient.ID, ownedGiftsNow-86401); err != nil {
		t.Fatal(err)
	}
	bot, _, err := botStore.CreateBotAccount(f.ctx, domain.User{FirstName: "Owned Gifts", Username: fmt.Sprintf("owned%d_bot", suffix), AccessHash: suffix + 10}, domain.BotProfile{OwnerUserID: f.owner.ID, TokenSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	f.bot = bot
	// Gift references are owner-local message IDs, not gift table row IDs.
	for range 3 {
		if _, err := f.messages.Create(f.ctx, domain.Message{OwnerUserID: f.owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.sender.ID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: f.sender.ID}, Body: "unrelated", Date: ownedGiftsNow - 200}); err != nil {
			t.Fatal(err)
		}
	}
	bots := appbots.NewService(f.users, botStore, f.messages)
	users := appusers.NewService(f.users)
	f.r = New(Config{}, Deps{Users: users, Bots: bots, Gifts: appgifts.NewService(f.gifts, nil, 2, giftsOptions...), Account: f.account, Channels: f.channels}, zaptest.NewLogger(t), fixedClock{now: time.Unix(ownedGiftsNow, 0)})
	f.rights(t, domain.BusinessBotRights{ViewGifts: true, SellGifts: true, TransferAndUpgradeGifts: true, TransferStars: true})
	if _, _, err := f.stars.EnsureGrant(f.ctx, f.owner.ID, 1000, ownedGiftsNow-100); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	server, err := botapi.Start(ctx, "127.0.0.1:0", bots, users, nil, nil, f.r, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = server.Close() })
	f.h = server.Handler
	return f
}

func (f *ownedGiftFixture) rights(t *testing.T, rights domain.BusinessBotRights) {
	t.Helper()
	connection, err := f.account.SaveConnectedBusinessBot(f.ctx, f.owner.ID, domain.ConnectedBusinessBot{BotUserID: f.bot.ID,
		Recipients: domain.BusinessBotRecipients{ExistingChats: true, NewChats: true, Contacts: true, NonContacts: true}, Rights: rights})
	if err != nil {
		t.Fatal(err)
	}
	f.connection = connection.ConnectionID
}

func ownedGiftRPCAttribute(t *testing.T, kind domain.StarGiftCollectibleAttributeKind, id int64, name string) domain.StarGiftCollectibleAttribute {
	t.Helper()
	attribute := collectibleRPCAttribute(kind, id, name)
	animation, err := appgifts.NewService(nil, nil, 2).PrepareAnimation("gift.json", []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4,"nm":"gift"}],"assets":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(animation.TGS)
	attribute.Animation = &animation
	attribute.Document.Size = int64(len(animation.TGS))
	attribute.Blob = &domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", id), Backend: domain.MediaBackendLocalFS,
		ObjectKey: hex.EncodeToString(digest[:]), SHA256: digest[:], Size: int64(len(animation.TGS)),
	}
	return attribute
}

func (f *ownedGiftFixture) catalog(t *testing.T, limited, upgrade bool) domain.StarGift {
	t.Helper()
	f.docID += 16
	a := ownedGiftRPCAttribute(t, domain.StarGiftCollectibleModel, f.docID, "Gift")
	write := domain.StarGiftCatalogWrite{Title: "Comet", Stars: 50, ConvertStars: 25, Enabled: true, Limited: limited,
		Document: *a.Document, Blob: *a.Blob, Animation: *a.Animation, Actor: "test", CommandID: fmt.Sprintf("gift:%d", f.docID)}
	if upgrade {
		write.Stars = 150
	}
	if limited {
		write.AvailabilityTotal, write.AvailabilityRemains = 100, 100
	}
	entry, err := f.gifts.CreateCatalogRevision(f.ctx, write)
	if err != nil {
		t.Fatal(err)
	}
	if upgrade {
		model := ownedGiftRPCAttribute(t, domain.StarGiftCollectibleModel, f.docID+1, "Aurora")
		pattern := ownedGiftRPCAttribute(t, domain.StarGiftCollectiblePattern, f.docID+2, "Orbit")
		_, err := f.gifts.PublishCollectibleRevision(f.ctx, domain.StarGiftCollectibleWrite{GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: fmt.Sprintf("comet-%d", entry.Gift.ID),
			Models: []domain.StarGiftCollectibleAttribute{model}, Patterns: []domain.StarGiftCollectibleAttribute{pattern},
			Backdrops: []domain.StarGiftCollectibleAttribute{collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 1, "Midnight"), collectibleRPCAttribute(domain.StarGiftCollectibleBackdrop, 2, "Daylight")}, Actor: "test", CommandID: fmt.Sprintf("pool:%d", f.docID)})
		if err != nil {
			t.Fatal(err)
		}
	}
	gift, found, err := f.gifts.CatalogGift(f.ctx, entry.Gift.ID)
	if err != nil || !found {
		t.Fatalf("catalog: %v %v", found, err)
	}
	return gift
}

func (f *ownedGiftFixture) receive(t *testing.T, gift domain.StarGift, date int, hidden, unsaved bool, prepaid int64) domain.SavedStarGift {
	t.Helper()
	sticker := gift.Sticker
	sent, err := f.messages.SendPrivateText(f.ctx, domain.SendPrivateTextRequest{SenderUserID: f.sender.ID, RecipientUserID: f.owner.ID, RandomID: int64(date) ^ gift.ID<<32 ^ int64(prepaid), Date: date,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{GiftID: gift.ID, Stars: gift.Stars, ConvertStars: gift.ConvertStars, Title: gift.Title, Sticker: &sticker, Message: "secret", FromUserID: f.sender.ID, PeerUserID: f.owner.ID, NameHidden: hidden, Saved: !unsaved, CanUpgrade: gift.UpgradeStars > 0, PrepaidUpgrade: prepaid > 0, UpgradePriceStars: gift.UpgradeStars, UpgradeStars: prepaid}}}})
	if err != nil {
		t.Fatal(err)
	}
	saved := domain.SavedStarGift{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, FromUserID: f.sender.ID, GiftID: gift.ID, RevisionID: gift.RevisionID, MsgID: sent.RecipientMessage.ID, Date: date,
		NameHidden: hidden, Unsaved: unsaved, ConvertStars: gift.ConvertStars, Message: "secret", PrepaidUpgradeStars: prepaid}
	id, err := f.gifts.Create(f.ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	saved.ID, saved.LifecycleStatus = id, domain.StarGiftLifecycleActive
	return saved
}

func (f *ownedGiftFixture) call(t *testing.T, method string, input map[string]any, wantOK bool) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	f.h.ServeHTTP(rec, req)
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.OK != wantOK {
		t.Fatalf("%s: %s (%v)", method, rec.Body.String(), err)
	}
	return envelope.Result
}

func (f *ownedGiftFixture) page(t *testing.T, method string, input map[string]any) ownedGiftHTTPPage {
	t.Helper()
	// Decode each HTTP response independently: unmarshaling into a reused
	// []map retains omitted keys from the previous page's map elements.
	var page ownedGiftHTTPPage
	body := f.call(t, method, input, true)
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("%s page: %s (%v)", method, body, err)
	}
	return page
}

func (f *ownedGiftFixture) command(saved domain.SavedStarGift) map[string]any {
	return map[string]any{"business_connection_id": f.connection, "owned_gift_id": strconv.Itoa(saved.MsgID)}
}
func (f *ownedGiftFixture) assertBalance(t *testing.T, want int64) {
	t.Helper()
	got, err := f.stars.GetBalance(f.ctx, f.owner.ID)
	if err != nil || got.Balance != want {
		t.Fatalf("balance=%+v err=%v want=%d", got, err, want)
	}
}

func TestBotAPIOwnedGiftPrivacyAndPagination(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newOwnedGiftFixture(t, backend)
			unlimited, limited, nonUpgrade := f.catalog(t, false, false), f.catalog(t, true, true), f.catalog(t, true, false)
			first := f.receive(t, unlimited, ownedGiftsNow-40, false, false, 0)
			f.receive(t, limited, ownedGiftsNow-30, true, false, 0)
			f.receive(t, nonUpgrade, ownedGiftsNow-20, false, false, 0)
			f.receive(t, limited, ownedGiftsNow-10, false, true, 0)
			if err := f.gifts.SetPinned(f.ctx, first.Owner, []int64{first.ID}); err != nil {
				t.Fatal(err)
			}
			page := f.page(t, "getUserGifts", map[string]any{"user_id": f.owner.ID, "limit": 1})
			if page.Count != 3 || len(page.Gifts) != 1 || page.Gifts[0]["send_date"] != float64(ownedGiftsNow-20) || page.Next == "" {
				t.Fatalf("first page=%+v", page)
			}
			page = f.page(t, "getUserGifts", map[string]any{"user_id": f.owner.ID, "limit": 1, "offset": page.Next})
			if len(page.Gifts) != 1 || page.Gifts[0]["is_private"] != true || page.Gifts[0]["sender_user"] != nil || page.Gifts[0]["text"] != nil || page.Gifts[0]["owned_gift_id"] != nil {
				t.Fatalf("private gift leaked: %+v", page)
			}
			page = f.page(t, "getUserGifts", map[string]any{"user_id": f.owner.ID, "limit": 1, "offset": page.Next})
			if len(page.Gifts) != 1 || page.Gifts[0]["send_date"] != float64(first.Date) || page.Next != "" {
				t.Fatalf("last page=%+v", page)
			}
			page = f.page(t, "getUserGifts", map[string]any{"user_id": f.owner.ID, "sort_by_price": true, "limit": 1})
			if len(page.Gifts) != 1 || page.Gifts[0]["send_date"] != float64(ownedGiftsNow-30) || page.Next == "" {
				t.Fatalf("price order must precede pagination: %+v", page)
			}
			for _, flags := range []map[string]any{{"exclude_unlimited": true}, {"exclude_limited_upgradable": true}, {"exclude_limited_non_upgradable": true}} {
				flags["user_id"] = f.owner.ID
				page = f.page(t, "getUserGifts", flags)
				if page.Count != 2 {
					t.Fatalf("filtered count=%d flags=%v", page.Count, flags)
				}
			}
			f.call(t, "getUserGifts", map[string]any{"user_id": f.owner.ID, "offset": "invalid"}, false)
			page = f.page(t, "getBusinessAccountGifts", map[string]any{"business_connection_id": f.connection})
			if page.Count != 4 {
				t.Fatalf("business cannot see hidden gift: %+v", page)
			}
		})
	}
}

func TestBotAPIOwnedGiftBalanceUpgradeAndTransfer(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newOwnedGiftFixture(t, backend)
			regular := f.catalog(t, false, false)
			convert := f.receive(t, regular, ownedGiftsNow-50, false, false, 0)
			f.rights(t, domain.BusinessBotRights{ViewGifts: true})
			f.call(t, "convertGiftToStars", f.command(convert), false)
			f.assertBalance(t, 1000)
			f.rights(t, domain.BusinessBotRights{ViewGifts: true, SellGifts: true, TransferAndUpgradeGifts: true, TransferStars: true})
			wrongID := f.command(convert)
			wrongID["owned_gift_id"] = strconv.FormatInt(convert.ID, 10)
			f.call(t, "convertGiftToStars", wrongID, false)
			f.assertBalance(t, 1000)
			wrongConnection := f.command(convert)
			wrongConnection["business_connection_id"] = f.connection + "invalid"
			f.call(t, "convertGiftToStars", wrongConnection, false)
			f.assertBalance(t, 1000)
			f.call(t, "convertGiftToStars", f.command(convert), true)
			f.assertBalance(t, 1025)
			f.call(t, "convertGiftToStars", f.command(convert), false)
			f.assertBalance(t, 1025)
			gift := f.catalog(t, true, true)
			saved := f.receive(t, gift, ownedGiftsNow-40, false, false, 0)
			upgrade := f.command(saved)
			upgrade["star_count"], upgrade["keep_original_details"] = 99, true
			f.call(t, "upgradeGift", upgrade, false)
			f.assertBalance(t, 1025)
			upgrade["star_count"] = 100
			f.rights(t, domain.BusinessBotRights{ViewGifts: true, TransferAndUpgradeGifts: true})
			f.call(t, "upgradeGift", upgrade, false)
			f.assertBalance(t, 1025)
			f.rights(t, domain.BusinessBotRights{ViewGifts: true, SellGifts: true, TransferAndUpgradeGifts: true, TransferStars: true})
			f.call(t, "upgradeGift", upgrade, true)
			f.assertBalance(t, 925)
			f.call(t, "upgradeGift", upgrade, false)
			f.assertBalance(t, 925)
			upgraded, found, err := f.gifts.GetByRef(f.ctx, starGiftUpgradeSavedRef(saved))
			if err != nil || !found || upgraded.UniqueGiftID == 0 {
				t.Fatalf("upgrade missing: %+v %v", upgraded, err)
			}
			unique, found, err := f.gifts.UniqueByID(f.ctx, upgraded.UniqueGiftID)
			if err != nil || !found || !unique.KeepOriginalDetails || unique.OriginalMessage != "secret" || unique.Owner != saved.Owner {
				t.Fatalf("unique=%+v %v", unique, err)
			}
			transfer := f.command(saved)
			transfer["new_owner_chat_id"], transfer["star_count"] = f.recipient.ID, 25
			if err := f.r.deps.Users.(*appusers.Service).UpdateLastSeen(f.ctx, f.recipient.ID, ownedGiftsNow-86401); err != nil {
				t.Fatal(err)
			}
			f.call(t, "transferGift", transfer, false)
			f.assertBalance(t, 925)
			if err := f.r.deps.Users.(*appusers.Service).UpdateLastSeen(f.ctx, f.recipient.ID, ownedGiftsNow); err != nil {
				t.Fatal(err)
			}
			settings, err := f.account.SetGlobalPrivacy(f.ctx, f.recipient.ID, domain.GlobalPrivacy{DisallowedGifts: domain.DisallowedGifts{UniqueStargifts: true}})
			if err != nil {
				t.Fatal(err)
			}
			f.r.accountSettings.Store(f.recipient.ID, settings)
			f.call(t, "transferGift", transfer, false)
			f.assertBalance(t, 925)
			settings, err = f.account.SetGlobalPrivacy(f.ctx, f.recipient.ID, domain.GlobalPrivacy{})
			if err != nil {
				t.Fatal(err)
			}
			f.r.accountSettings.Store(f.recipient.ID, settings)
			f.call(t, "transferGift", transfer, true)
			f.assertBalance(t, 900)
			f.call(t, "transferGift", transfer, false)
			f.assertBalance(t, 900)
			unique, _, err = f.gifts.UniqueByID(f.ctx, unique.ID)
			if err != nil || unique.Owner.ID != f.recipient.ID {
				t.Fatalf("new owner=%+v err=%v", unique.Owner, err)
			}
			if _, found, _ := f.gifts.GetByRef(f.ctx, starGiftUpgradeSavedRef(saved)); found {
				t.Fatal("old owner retained transferable reference")
			}
			var public struct {
				Gifts []struct {
					Type string `json:"type"`
					Gift struct {
						Name  string `json:"name"`
						Model struct {
							Sticker map[string]any `json:"sticker"`
						} `json:"model"`
						Symbol struct {
							Sticker map[string]any `json:"sticker"`
						} `json:"symbol"`
						Backdrop struct {
							Colors map[string]any `json:"colors"`
						} `json:"backdrop"`
					} `json:"gift"`
				} `json:"gifts"`
			}
			if err := json.Unmarshal(f.call(t, "getUserGifts", map[string]any{"user_id": f.recipient.ID}, true), &public); err != nil {
				t.Fatal(err)
			}
			if len(public.Gifts) != 1 || public.Gifts[0].Type != "unique" || public.Gifts[0].Gift.Name != unique.Slug ||
				public.Gifts[0].Gift.Model.Sticker["file_id"] == nil || public.Gifts[0].Gift.Symbol.Sticker["file_id"] == nil || public.Gifts[0].Gift.Backdrop.Colors["center_color"] == nil {
				t.Fatalf("incomplete unique gift projection: %+v", public)
			}
			prepaid := f.receive(t, gift, ownedGiftsNow-20, false, false, 100)
			f.rights(t, domain.BusinessBotRights{ViewGifts: true, TransferAndUpgradeGifts: true})
			f.call(t, "upgradeGift", f.command(prepaid), true)
			f.assertBalance(t, 900)
			pool, found, err := f.gifts.ActiveCollectibleRevision(f.ctx, gift.ID)
			if err != nil || !found || pool.Issued != 2 {
				t.Fatalf("issuance=%+v err=%v", pool, err)
			}
			insufficient := f.receive(t, gift, ownedGiftsNow-5, false, false, 0)
			if _, err := f.stars.Debit(f.ctx, f.owner.ID, 900, domain.StarsReasonGift, domain.Peer{}, ownedGiftsNow, "spend", ""); err != nil {
				t.Fatal(err)
			}
			f.rights(t, domain.BusinessBotRights{ViewGifts: true, TransferAndUpgradeGifts: true, TransferStars: true})
			input := f.command(insufficient)
			input["star_count"] = 100
			f.call(t, "upgradeGift", input, false)
			f.assertBalance(t, 0)
			unmodified, found, err := f.gifts.GetByRef(f.ctx, starGiftUpgradeSavedRef(insufficient))
			if err != nil || !found || unmodified.UniqueGiftID != 0 {
				t.Fatalf("failed upgrade changed gift: %+v %v", unmodified, err)
			}
			pool, found, err = f.gifts.ActiveCollectibleRevision(f.ctx, gift.ID)
			if err != nil || !found || pool.Issued != 2 {
				t.Fatalf("failed upgrade consumed issuance: %+v %v", pool, err)
			}
		})
	}
}

func TestBotAPIChatGiftVisibilityRequiresPostMessages(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newOwnedGiftFixture(t, backend)
			created, err := f.channels.CreateChannel(f.ctx, f.owner.ID, domain.CreateChannelRequest{Title: "Gift channel", Broadcast: true, MemberUserIDs: []int64{f.bot.ID}, Date: ownedGiftsNow - 100})
			if err != nil {
				t.Fatal(err)
			}
			gift := f.catalog(t, false, false)
			owner := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
			for i, hidden := range []bool{false, true} {
				if _, err := f.gifts.Create(f.ctx, domain.SavedStarGift{Owner: owner, FromUserID: f.sender.ID, GiftID: gift.ID, RevisionID: gift.RevisionID, Date: ownedGiftsNow - i, Unsaved: hidden}); err != nil {
					t.Fatal(err)
				}
			}
			input := map[string]any{"chat_id": -botAPIChannelChatIDBase - created.Channel.ID, "exclude_unsaved": false, "exclude_saved": true}
			var page struct {
				Count int `json:"total_count"`
			}
			json.Unmarshal(f.call(t, "getChatGifts", input, true), &page)
			if page.Count != 1 {
				t.Fatalf("nonadmin count=%d", page.Count)
			}
			if _, err := f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{ChannelID: created.Channel.ID, MemberID: f.bot.ID, AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: ownedGiftsNow}); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(f.call(t, "getChatGifts", input, true), &page)
			if page.Count != 1 {
				t.Fatalf("admin without post right count=%d", page.Count)
			}
			if _, err := f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{ChannelID: created.Channel.ID, MemberID: f.bot.ID, AdminRights: domain.ChannelAdminRights{PostMessages: true}, Date: ownedGiftsNow}); err != nil {
				t.Fatal(err)
			}
			input["exclude_saved"] = false
			json.Unmarshal(f.call(t, "getChatGifts", input, true), &page)
			if page.Count != 2 {
				t.Fatalf("post admin count=%d", page.Count)
			}
		})
	}
}

func TestBotAPIGiftConcurrentConversionCreditsOnce(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			f := newOwnedGiftFixture(t, backend)
			saved := f.receive(t, f.catalog(t, false, false), ownedGiftsNow-10, false, false, 0)
			results := make(chan bool, 8)
			for range 8 {
				go func() {
					ok, err := f.r.BotAPIConvertGiftToStars(f.ctx, f.bot.ID, f.connection, strconv.Itoa(saved.MsgID))
					results <- ok && err == nil
				}()
			}
			successes := 0
			for range 8 {
				if <-results {
					successes++
				}
			}
			if successes != 1 {
				t.Fatalf("successful conversions=%d, want 1", successes)
			}
			f.assertBalance(t, 1025)
			page, err := f.gifts.ListByOwner(f.ctx, saved.Owner, false, "", 10)
			if err != nil || page.Count != 0 {
				t.Fatalf("converted gift remains visible: %+v %v", page, err)
			}
		})
	}
}

func TestBotAPIGiftDeliveryFailureDoesNotSpendOrMint(t *testing.T) {
	f := newOwnedGiftFixture(t, "memory")
	gift := f.catalog(t, true, true)
	saved := f.receive(t, gift, ownedGiftsNow-10, false, false, 0)
	f.messages.(*memory.MessageStore).AttachUpdateEventStore(nil)
	input := f.command(saved)
	input["star_count"] = 100
	f.call(t, "upgradeGift", input, false)
	f.assertBalance(t, 1000)
	current, found, err := f.gifts.GetByRef(f.ctx, starGiftUpgradeSavedRef(saved))
	if err != nil || !found || current.UniqueGiftID != 0 {
		t.Fatalf("failed delivery changed gift: %+v %v", current, err)
	}
	pool, found, err := f.gifts.ActiveCollectibleRevision(f.ctx, gift.ID)
	if err != nil || !found || pool.Issued != 0 {
		t.Fatalf("failed delivery consumed issuance: %+v %v", pool, err)
	}
}
