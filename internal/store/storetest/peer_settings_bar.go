package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// PeerSettingsBarStore 是记录"已关闭会话提示栏"的存储（memory 与 postgres 的 DialogStore）。
type PeerSettingsBarStore interface {
	SetPeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	PeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
}

// PeerSettingsBarContract：没有对话记录（从联系人列表点进还没聊过的人）也能关闭提示栏
// 并读回；重复关闭报告无变化；按用户、按 peer 隔离。user/other 是两个已存在的用户。
func PeerSettingsBarContract(t *testing.T, s PeerSettingsBarStore, user, other int64) {
	t.Helper()
	ctx := context.Background()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: other}
	hidden := func(owner int64, p domain.Peer) bool {
		t.Helper()
		got, err := s.PeerSettingsBarHidden(ctx, owner, p)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if hidden(user, peer) {
		t.Fatal("fresh peer: hidden = true, want false")
	}
	if changed, err := s.SetPeerSettingsBarHidden(ctx, user, peer); err != nil || !changed {
		t.Fatalf("hide without a dialog = changed %v err %v, want changed", changed, err)
	}
	if !hidden(user, peer) {
		t.Fatal("after hide: hidden = false, want true")
	}
	if changed, err := s.SetPeerSettingsBarHidden(ctx, user, peer); err != nil || changed {
		t.Fatalf("hide again = changed %v err %v, want unchanged", changed, err)
	}
	if hidden(other, domain.Peer{Type: domain.PeerTypeUser, ID: user}) {
		t.Fatal("other user's bar hidden too, want isolated")
	}
	if hidden(user, domain.Peer{Type: domain.PeerTypeUser, ID: other + 1}) {
		t.Fatal("another peer hidden too, want isolated")
	}
}
