package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// NotifySettingsBatchStore 是按一组 owner 批量读取通知设置的存储（memory 与 postgres 的 PasswordStore）。
type NotifySettingsBatchStore interface {
	SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error)
	GetNotifySettingsForOwners(ctx context.Context, ownerUserIDs []int64, scope domain.NotifyScope) (map[int64]domain.PeerNotifySettings, error)
}

// NotifySettingsBatchContract：批量读取与逐个读取一致——只返回设置过该作用域的 owner，
// 不混入其他作用域（整 peer 与话题、默认范围）或其他 owner。owners 是三个互不相同、没有任何设置的用户。
func NotifySettingsBatchContract(t *testing.T, s NotifySettingsBatchStore, owners [3]int64) {
	t.Helper()
	ctx := context.Background()
	group := domain.Peer{Type: domain.PeerTypeChannel, ID: owners[0] + 7}
	peerScope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: group}
	topicScope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: group, TopicID: 3}
	chatsScope := domain.NotifyScope{Kind: domain.NotifyScopeChats}
	mute, unmute, hide := 2147483647, 0, false
	save := func(owner int64, scope domain.NotifyScope, v domain.PeerNotifySettings) {
		t.Helper()
		if err := s.SaveNotifySettings(ctx, owner, scope, v); err != nil {
			t.Fatal(err)
		}
	}
	save(owners[0], peerScope, domain.PeerNotifySettings{MuteUntil: &mute})
	save(owners[1], peerScope, domain.PeerNotifySettings{ShowPreviews: &hide})
	save(owners[1], topicScope, domain.PeerNotifySettings{MuteUntil: &unmute})
	save(owners[2], chatsScope, domain.PeerNotifySettings{MuteUntil: &mute})

	check := func(scope domain.NotifyScope, want map[int64]domain.PeerNotifySettings) {
		t.Helper()
		got, err := s.GetNotifySettingsForOwners(ctx, owners[:], scope)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("scope %+v: got %d owners %+v, want %d", scope, len(got), got, len(want))
		}
		for owner, w := range want {
			g, ok := got[owner]
			if !ok || !sameNotifySettings(g, w) {
				t.Fatalf("scope %+v owner %d: got %+v (found %v), want %+v", scope, owner, g, ok, w)
			}
			single, found, err := s.GetNotifySettings(ctx, owner, scope)
			if err != nil || !found || !sameNotifySettings(single, g) {
				t.Fatalf("scope %+v owner %d: batch %+v differs from single %+v (found %v, err %v)", scope, owner, g, single, found, err)
			}
		}
	}
	check(peerScope, map[int64]domain.PeerNotifySettings{owners[0]: {MuteUntil: &mute}, owners[1]: {ShowPreviews: &hide}})
	check(topicScope, map[int64]domain.PeerNotifySettings{owners[1]: {MuteUntil: &unmute}})
	check(chatsScope, map[int64]domain.PeerNotifySettings{owners[2]: {MuteUntil: &mute}})
	check(domain.NotifyScope{Kind: domain.NotifyScopeBroadcasts}, map[int64]domain.PeerNotifySettings{})
	if got, err := s.GetNotifySettingsForOwners(ctx, nil, peerScope); err != nil || len(got) != 0 {
		t.Fatalf("no owners = %+v err=%v, want empty", got, err)
	}
}

func sameNotifySettings(a, b domain.PeerNotifySettings) bool {
	eqInt := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqBool := func(x, y *bool) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return eqInt(a.MuteUntil, b.MuteUntil) && eqBool(a.ShowPreviews, b.ShowPreviews) && eqBool(a.Silent, b.Silent)
}
