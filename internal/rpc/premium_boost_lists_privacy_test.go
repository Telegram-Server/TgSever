package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// boostPrivacyBackend 是 boost 列表可见性场景所用的存储后端：同一组断言分别跑在
// memory 和 postgres 上，锁定两个后端输出的 boost 用户一致（#40）。
type boostPrivacyBackend struct {
	users    store.UserStore
	channels store.ChannelStore
	// phone 为第 i 个用户生成该后端内唯一的手机号。
	phone func(i int) string
	// createDeleted 建一个已注销的用户（memory 原始行保留资料，postgres 按约束清空）。
	createDeleted func(t *testing.T, u domain.User) domain.User
}

// boostPrivacyFixture：频道群主 owner 是 booster 的联系人；strangerAdmin 是 booster
// 的陌生人但同为管理员。booster 的手机号与头像都设为「仅联系人」，并有一张 profile
// 头像；gone 给频道 boost 后注销了账号。
type boostPrivacyFixture struct {
	r             *Router
	channel       domain.Channel
	owner         domain.User
	strangerAdmin domain.User
	booster       domain.User
	gone          domain.User
	photo         domain.Photo
}

func newBoostPrivacyFixture(t *testing.T, backend boostPrivacyBackend) boostPrivacyFixture {
	t.Helper()
	ctx := context.Background()
	mustCreate := func(i int, name string) domain.User {
		t.Helper()
		u, err := backend.users.Create(ctx, domain.User{AccessHash: int64(4000 + i), Phone: backend.phone(i), FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	owner := mustCreate(1, "Owner")
	strangerAdmin := mustCreate(2, "StrangerAdmin")
	booster := mustCreate(3, "Booster")
	gone := backend.createDeleted(t, domain.User{AccessHash: 4004, Phone: backend.phone(4), FirstName: "Gone"})

	now := int(time.Now().Unix())
	created, err := backend.channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Boost privacy",
		Broadcast:     true,
		MemberUserIDs: []int64{strangerAdmin.ID, booster.ID, gone.ID},
		Date:          now,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.Channel
	if _, err := backend.channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID: owner.ID, ChannelID: channel.ID, MemberID: strangerAdmin.ID,
		AdminRights: domain.ChannelAdminRights{ChangeInfo: true}, Date: now,
	}); err != nil {
		t.Fatalf("promote stranger admin: %v", err)
	}
	for _, u := range []domain.User{booster, gone} {
		if _, err := backend.channels.ApplyPremiumBoost(ctx, u.ID, channel.ID, []int{domain.DefaultPremiumBoostSlotID}, now, now+3600); err != nil {
			t.Fatalf("%s boosts channel: %v", u.FirstName, err)
		}
	}

	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts)
	if _, err := contacts.Upsert(ctx, booster.ID, domain.ContactInput{ContactUserID: owner.ID, FirstName: "Owner"}); err != nil {
		t.Fatalf("booster adds owner as contact: %v", err)
	}
	for _, key := range []domain.PrivacyKey{domain.PrivacyKeyPhoneNumber, domain.PrivacyKeyProfilePhoto} {
		if _, err := privacy.SetRules(ctx, booster.ID, key, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
			t.Fatalf("set booster %s privacy: %v", key, err)
		}
	}
	files := &fakeFiles{}
	photo := files.putPhoto(albumTestPhoto(9100000000000004001, 4001))
	files.setCurrentProfile(domain.PeerTypeUser, booster.ID, domain.ProfilePhotoKindProfile, photo.ID)

	r := New(Config{}, Deps{
		Users: appusers.NewService(backend.users,
			appusers.WithContactStore(contacts),
			appusers.WithPrivacyEvaluator(privacy),
			appusers.WithPhotoProvider(files),
		),
		Channels: appchannels.NewService(backend.channels),
		Privacy:  privacy,
	}, zaptest.NewLogger(t), clock.System)
	return boostPrivacyFixture{r: r, channel: channel, owner: owner, strangerAdmin: strangerAdmin, booster: booster, gone: gone, photo: photo}
}

func (f boostPrivacyFixture) peer() *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

// boostLists 以 viewer 身份调用所有返回 boost 用户的列表接口。
func (f boostPrivacyFixture) boostLists(t *testing.T, viewer domain.User) map[string]*tg.PremiumBoostsList {
	t.Helper()
	ctx := WithUserID(context.Background(), viewer.ID)
	list, err := f.r.onPremiumGetBoostsList(ctx, &tg.PremiumGetBoostsListRequest{Peer: f.peer(), Limit: 10})
	if err != nil {
		t.Fatalf("premium.getBoostsList as %s: %v", viewer.FirstName, err)
	}
	userBoosts, err := f.r.onPremiumGetUserBoosts(ctx, &tg.PremiumGetUserBoostsRequest{
		Peer:   f.peer(),
		UserID: &tg.InputUser{UserID: f.booster.ID, AccessHash: f.booster.AccessHash},
	})
	if err != nil {
		t.Fatalf("premium.getUserBoosts as %s: %v", viewer.FirstName, err)
	}
	return map[string]*tg.PremiumBoostsList{
		"premium.getBoostsList": list,
		"premium.getUserBoosts": userBoosts,
	}
}

func boostListUser(t *testing.T, method string, list *tg.PremiumBoostsList, userID int64) *tg.User {
	t.Helper()
	for _, raw := range list.Users {
		if u, ok := raw.(*tg.User); ok && u.ID == userID {
			return u
		}
	}
	t.Fatalf("%s users = %+v, missing user %d", method, list.Users, userID)
	return nil
}

func boostUserPhotoID(u *tg.User) int64 {
	if p, ok := u.Photo.(*tg.UserProfilePhoto); ok {
		return p.PhotoID
	}
	return 0
}

func runBoostListPrivacy(t *testing.T, backend func(t *testing.T) boostPrivacyBackend) {
	t.Run("stranger admin sees no hidden phone or photo", func(t *testing.T) {
		f := newBoostPrivacyFixture(t, backend(t))
		for method, list := range f.boostLists(t, f.strangerAdmin) {
			u := boostListUser(t, method, list, f.booster.ID)
			if phone := u.Phone; phone != "" {
				t.Fatalf("%s booster phone = %q, want hidden from stranger admin", method, phone)
			}
			if id := boostUserPhotoID(u); id != 0 {
				t.Fatalf("%s booster photo = %d, want hidden from stranger admin", method, id)
			}
		}
	})
	t.Run("contact admin still sees phone and photo", func(t *testing.T) {
		f := newBoostPrivacyFixture(t, backend(t))
		for method, list := range f.boostLists(t, f.owner) {
			u := boostListUser(t, method, list, f.booster.ID)
			if phone := u.Phone; phone != f.booster.Phone {
				t.Fatalf("%s booster phone = %q, want %q for contact admin", method, phone, f.booster.Phone)
			}
			if id := boostUserPhotoID(u); id != f.photo.ID {
				t.Fatalf("%s booster photo = %d, want %d for contact admin", method, id, f.photo.ID)
			}
		}
	})
	t.Run("user boosts carry the queried user even without boosts", func(t *testing.T) {
		f := newBoostPrivacyFixture(t, backend(t))
		got, err := f.r.onPremiumGetUserBoosts(WithUserID(context.Background(), f.strangerAdmin.ID), &tg.PremiumGetUserBoostsRequest{
			Peer:   f.peer(),
			UserID: &tg.InputUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash},
		})
		if err != nil {
			t.Fatalf("premium.getUserBoosts for non-booster: %v", err)
		}
		if got.Count != 0 || len(got.Boosts) != 0 {
			t.Fatalf("premium.getUserBoosts for non-booster = %+v, want no boosts", got)
		}
		boostListUser(t, "premium.getUserBoosts", got, f.owner.ID)
	})
	t.Run("deleted booster is a tombstone", func(t *testing.T) {
		f := newBoostPrivacyFixture(t, backend(t))
		for _, viewer := range []domain.User{f.owner, f.strangerAdmin} {
			list := f.boostLists(t, viewer)["premium.getBoostsList"]
			u := boostListUser(t, "premium.getBoostsList", list, f.gone.ID)
			// 墓碑带的是统一的占位名，不能是原始行里的名字。
			if phone := u.Phone; !u.Deleted || phone != "" || u.FirstName == f.gone.FirstName {
				t.Fatalf("deleted booster seen by %s = deleted %v phone %q name %q, want tombstone", viewer.FirstName, u.Deleted, u.Phone, u.FirstName)
			}
		}
	})
}

func TestPremiumBoostListsProjectUsersForViewer(t *testing.T) {
	runBoostListPrivacy(t, func(t *testing.T) boostPrivacyBackend {
		users := memory.NewUserStore()
		return boostPrivacyBackend{
			users:    users,
			channels: memory.NewChannelStore(),
			phone:    func(i int) string { return fmt.Sprintf("1555000400%d", i) },
			createDeleted: func(t *testing.T, u domain.User) domain.User {
				t.Helper()
				// 墓碑是读取期投影：memory 原始行保留手机号与名字。
				u.Deleted = true
				u.DeletedAt = time.Now().Unix()
				created, err := users.Create(context.Background(), u)
				if err != nil {
					t.Fatalf("create deleted user: %v", err)
				}
				return created
			},
		}
	})
}
