package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func folderRequest(id int) *tg.MessagesUpdateDialogFilterRequest {
	req := &tg.MessagesUpdateDialogFilterRequest{ID: id}
	req.SetFilter(&tg.DialogFilter{ID: id, Contacts: true, Title: tg.TextWithEntities{Text: fmt.Sprintf("F%d", id)}})
	return req
}

// 接线：updateDialogFilter 按会员身份取文件夹数上限，新建超限回 DIALOG_FILTERS_TOO_MUCH，
// 已有文件夹照常可改。上限本身的矩阵（并发、按用户计、删除腾名额）在存储契约里。
func TestUpdateDialogFilterEnforcesFolderLimitByPremium(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	regular, _ := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550007101", FirstName: "Regular"})
	premium, _ := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550007102", FirstName: "Premium",
		PremiumUntil: int(time.Now().Add(72 * time.Hour).Unix())})
	r := New(Config{}, Deps{
		Users:   appusers.NewService(users),
		Dialogs: appdialogs.NewService(memory.NewDialogStore()),
	}, zaptest.NewLogger(t), clock.System)

	for _, tt := range []struct {
		name   string
		userID int64
		limit  int
	}{
		{name: "regular", userID: regular.ID, limit: domain.DialogFoldersLimitDefault},
		{name: "premium", userID: premium.ID, limit: domain.DialogFoldersLimitPremium},
	} {
		t.Run(tt.name, func(t *testing.T) {
			userCtx := WithUserID(ctx, tt.userID)
			first := domain.DialogCustomFolderMinID
			for id := first; id < first+tt.limit; id++ {
				if ok, err := r.onMessagesUpdateDialogFilter(userCtx, folderRequest(id)); err != nil || !ok {
					t.Fatalf("create folder %d of %d: ok=%v err=%v", id-first+1, tt.limit, ok, err)
				}
			}
			over := first + tt.limit
			if _, err := r.onMessagesUpdateDialogFilter(userCtx, folderRequest(over)); !tgerr.Is(err, "DIALOG_FILTERS_TOO_MUCH") {
				t.Fatalf("folder %d over the limit: err = %v, want DIALOG_FILTERS_TOO_MUCH", tt.limit+1, err)
			}
			if ok, err := r.onMessagesUpdateDialogFilter(userCtx, folderRequest(first)); err != nil || !ok {
				t.Fatalf("edit an existing folder at the limit: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestCheckDialogFolderChats(t *testing.T) {
	peers := func(from, n int) []domain.DialogFolderPeer {
		out := make([]domain.DialogFolderPeer, n)
		for i := range out {
			out[i] = domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: int64(from + i)}}
		}
		return out
	}
	for _, premium := range []bool{false, true} {
		limit := domain.DialogFolderChatsLimit(premium)
		t.Run(fmt.Sprintf("premium=%v", premium), func(t *testing.T) {
			if err := checkDialogFolderChats(nil, peers(1, limit), peers(10_000, limit), limit); err != nil {
				t.Fatalf("include and exclude at %d each: %v", limit, err)
			}
			// 置顶会话也是收录的：与 include 重叠不重复计，不重叠则合计。
			if err := checkDialogFolderChats(peers(1, 5), peers(1, limit), nil, limit); err != nil {
				t.Fatalf("pinned overlapping include: %v", err)
			}
			if err := checkDialogFolderChats(peers(1, 5), peers(100, limit-4), nil, limit); !tgerr.Is(err, "LIMIT_INVALID") {
				t.Fatalf("pinned + include = %d: err = %v, want LIMIT_INVALID", limit+1, err)
			}
			if err := checkDialogFolderChats(nil, nil, peers(1, limit+1), limit); !tgerr.Is(err, "LIMIT_INVALID") {
				t.Fatalf("exclude = %d: err = %v, want LIMIT_INVALID", limit+1, err)
			}
		})
	}
	if domain.DialogFolderChatsLimit(true) != 200 || domain.DialogFolderChatsLimit(false) != 100 {
		t.Fatalf("chats limits = %d/%d, want 100/200 to match appConfig", domain.DialogFolderChatsLimit(false), domain.DialogFolderChatsLimit(true))
	}
}
