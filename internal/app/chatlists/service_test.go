package chatlists

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/store/memory"
)

func TestSharedFolderInviteJoinUpdatesAndLeave(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	chatlists := memory.NewChatlistStore()
	svc := NewService(chatlists, dialogs, WithSlugGenerator(func() (string, error) { return "slug-one", nil }))

	ownerID := int64(1001)
	viewerID := int64(2002)
	peerA := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	peerB := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3002}, AccessHash: 32}
	peerC := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3003}, AccessHash: 33}

	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IncludePeers: []domain.DialogFolderPeer{peerA},
	}); err != nil {
		t.Fatalf("seed owner folder: %v", err)
	}

	folder, invite, err := svc.ExportInvite(ctx, ownerID, 2, "Main link", []domain.DialogFolderPeer{peerA}, 100)
	if err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	if invite.Slug != "slug-one" || !folder.IsChatlist || !folder.HasMyInvites {
		t.Fatalf("export = folder %+v invite %+v, want chatlist with invite slug", folder, invite)
	}
	persisted, found, err := dialogs.GetFolder(ctx, ownerID, 2)
	if err != nil || !found || !persisted.IsChatlist || !persisted.HasMyInvites {
		t.Fatalf("persisted folder = %+v found %v err %v, want exported chatlist", persisted, found, err)
	}

	preview, err := svc.CheckInvite(ctx, viewerID, "https://t.me/addlist/slug-one")
	if err != nil {
		t.Fatalf("CheckInvite before join: %v", err)
	}
	if len(preview.Missing) != 1 || preview.Missing[0].Peer != peerA.Peer || preview.LocalFolder != nil {
		t.Fatalf("preview before join = %+v, want missing peerA and no local folder", preview)
	}

	joined, err := svc.JoinInvite(ctx, viewerID, "slug-one", nil, 101)
	if err != nil {
		t.Fatalf("JoinInvite: %v", err)
	}
	if joined.Folder.ID != 2 || !joined.Folder.IsChatlist || joined.Folder.HasMyInvites || len(joined.Folder.IncludePeers) != 1 {
		t.Fatalf("joined folder = %+v, want imported local chatlist with peerA", joined.Folder)
	}

	preview, err = svc.CheckInvite(ctx, viewerID, "slug-one")
	if err != nil {
		t.Fatalf("CheckInvite after join: %v", err)
	}
	if preview.LocalFolder == nil || preview.Membership == nil || len(preview.Already) != 1 || len(preview.Missing) != 0 {
		t.Fatalf("preview after join = %+v, want already imported", preview)
	}

	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IsChatlist:   true,
		HasMyInvites: true,
		IncludePeers: []domain.DialogFolderPeer{peerA, peerB},
	}); err != nil {
		t.Fatalf("extend owner folder: %v", err)
	}
	if _, err := svc.EditInvite(ctx, ownerID, 2, "slug-one", nil, &[]domain.DialogFolderPeer{peerA, peerB}, false); err != nil {
		t.Fatalf("EditInvite add peerB: %v", err)
	}
	updates, err := svc.GetUpdates(ctx, viewerID, joined.Folder.ID)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates.Missing) != 1 || updates.Missing[0].Peer != peerB.Peer {
		t.Fatalf("updates = %+v, want missing peerB", updates)
	}
	ownerUpdates, err := svc.GetUpdates(ctx, ownerID, folder.ID)
	if err != nil {
		t.Fatalf("GetUpdates owner exported folder: %v", err)
	}
	if len(ownerUpdates.Missing) != 0 {
		t.Fatalf("owner updates = %+v, want empty", ownerUpdates)
	}
	updated, err := svc.JoinUpdates(ctx, viewerID, joined.Folder.ID, []domain.DialogFolderPeer{peerB}, 102)
	if err != nil {
		t.Fatalf("JoinUpdates: %v", err)
	}
	if len(updated.Folder.IncludePeers) != 2 {
		t.Fatalf("updated folder peers = %+v, want 2 peers", updated.Folder.IncludePeers)
	}

	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IsChatlist:   true,
		HasMyInvites: true,
		IncludePeers: []domain.DialogFolderPeer{peerA, peerB, peerC},
	}); err != nil {
		t.Fatalf("extend owner folder with peerC: %v", err)
	}
	if _, err := svc.EditInvite(ctx, ownerID, 2, "slug-one", nil, &[]domain.DialogFolderPeer{peerA, peerB, peerC}, false); err != nil {
		t.Fatalf("EditInvite add peerC: %v", err)
	}
	if err := svc.HideUpdates(ctx, viewerID, joined.Folder.ID); err != nil {
		t.Fatalf("HideUpdates: %v", err)
	}
	updates, err = svc.GetUpdates(ctx, viewerID, joined.Folder.ID)
	if err != nil {
		t.Fatalf("GetUpdates after hide: %v", err)
	}
	if len(updates.Missing) != 0 {
		t.Fatalf("updates after hide = %+v, want empty", updates)
	}

	suggestions, err := svc.LeaveSuggestions(ctx, viewerID, joined.Folder.ID)
	if err != nil {
		t.Fatalf("LeaveSuggestions: %v", err)
	}
	if len(suggestions) != 2 {
		t.Fatalf("leave suggestions = %+v, want current two imported peers", suggestions)
	}
	if _, err := svc.Leave(ctx, viewerID, joined.Folder.ID, suggestions, 103); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if _, found, err := dialogs.GetFolder(ctx, viewerID, joined.Folder.ID); err != nil || found {
		t.Fatalf("local folder after leave found=%v err=%v, want deleted", found, err)
	}
}

func TestDeleteLastSharedFolderInviteClearsOwnerFlag(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	chatlists := memory.NewChatlistStore()
	slugs := []string{"slug-a", "slug-b"}
	svc := NewService(chatlists, dialogs, WithSlugGenerator(func() (string, error) {
		slug := slugs[0]
		slugs = slugs[1:]
		return slug, nil
	}))

	ownerID := int64(1001)
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IncludePeers: []domain.DialogFolderPeer{peer},
	}); err != nil {
		t.Fatalf("seed owner folder: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "one", []domain.DialogFolderPeer{peer}, 100); err != nil {
		t.Fatalf("ExportInvite one: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "two", []domain.DialogFolderPeer{peer}, 101); err != nil {
		t.Fatalf("ExportInvite two: %v", err)
	}
	if folder, changed, err := svc.DeleteInvite(ctx, ownerID, 2, "slug-a"); err != nil || changed || folder.HasMyInvites {
		t.Fatalf("DeleteInvite first = folder %+v changed %v err %v, want no folder update", folder, changed, err)
	}
	persisted, found, err := dialogs.GetFolder(ctx, ownerID, 2)
	if err != nil || !found || !persisted.HasMyInvites {
		t.Fatalf("folder after first delete = %+v found %v err %v, want has_my_invites", persisted, found, err)
	}
	folder, changed, err := svc.DeleteInvite(ctx, ownerID, 2, "slug-b")
	if err != nil || !changed || folder.HasMyInvites {
		t.Fatalf("DeleteInvite last = folder %+v changed %v err %v, want cleared flag", folder, changed, err)
	}
	persisted, found, err = dialogs.GetFolder(ctx, ownerID, 2)
	if err != nil || !found || persisted.HasMyInvites {
		t.Fatalf("folder after last delete = %+v found %v err %v, want has_my_invites=false", persisted, found, err)
	}
}

func TestSharedFolderSlugValidation(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewChatlistStore(), memory.NewDialogStore())
	if _, err := svc.CheckInvite(ctx, 2002, "bad!"); !errors.Is(err, domain.ErrChatlistInviteInvalid) {
		t.Fatalf("CheckInvite bad slug err = %v, want ErrChatlistInviteInvalid", err)
	}
	longSlug := strings.Repeat("a", links.MaxChatlistSlugBytes+1)
	if _, err := svc.JoinInvite(ctx, 2002, longSlug, nil, 100); !errors.Is(err, domain.ErrChatlistInviteInvalid) {
		t.Fatalf("JoinInvite long slug err = %v, want ErrChatlistInviteInvalid", err)
	}
}

func TestRevokedSharedFolderInviteRemainsListedButCannotBeImported(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	chatlists := memory.NewChatlistStore()
	svc := NewService(chatlists, dialogs, WithSlugGenerator(func() (string, error) { return "slug-revoke", nil }))

	ownerID := int64(1001)
	viewerID := int64(2002)
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IncludePeers: []domain.DialogFolderPeer{peer},
	}); err != nil {
		t.Fatalf("seed owner folder: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "Main link", []domain.DialogFolderPeer{peer}, 100); err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	revoked, err := svc.EditInvite(ctx, ownerID, 2, "slug-revoke", nil, nil, true)
	if err != nil {
		t.Fatalf("EditInvite revoke: %v", err)
	}
	if !revoked.Revoked {
		t.Fatalf("revoked invite = %+v, want Revoked=true", revoked)
	}
	invites, err := svc.ListInvites(ctx, ownerID, 2)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 || !invites[0].Revoked {
		t.Fatalf("listed invites = %+v, want revoked invite visible to owner", invites)
	}
	if _, err := svc.CheckInvite(ctx, viewerID, "slug-revoke"); !errors.Is(err, domain.ErrChatlistInviteExpired) {
		t.Fatalf("CheckInvite revoked err = %v, want ErrChatlistInviteExpired", err)
	}
	if _, err := svc.JoinInvite(ctx, viewerID, "slug-revoke", nil, 101); !errors.Is(err, domain.ErrChatlistInviteExpired) {
		t.Fatalf("JoinInvite revoked err = %v, want ErrChatlistInviteExpired", err)
	}
}

func TestSharedFolderJoinUpdatesAndLeaveUseChannelMemberships(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	channels := &fakeChatlistChannels{}
	svc := NewService(
		memory.NewChatlistStore(),
		dialogs,
		WithChannels(channels),
		WithSlugGenerator(func() (string, error) { return "slug-channel", nil }),
	)

	ownerID := int64(1001)
	viewerID := int64(2002)
	peerA := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	peerB := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3002}, AccessHash: 32}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IncludePeers: []domain.DialogFolderPeer{peerA, peerB},
	}); err != nil {
		t.Fatalf("seed owner folder: %v", err)
	}

	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "Main link", []domain.DialogFolderPeer{peerA, peerB}, 100); err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	if got := channels.getIDs; len(got) != 2 || got[0] != peerA.Peer.ID || got[1] != peerB.Peer.ID {
		t.Fatalf("shareability channel checks = %v, want peerA/peerB", got)
	}

	joined, err := svc.JoinInvite(ctx, viewerID, "slug-channel", []domain.DialogFolderPeer{peerA}, 101)
	if err != nil {
		t.Fatalf("JoinInvite peerA: %v", err)
	}
	if got := channels.inviteIDs; len(got) != 1 || got[0] != peerA.Peer.ID {
		t.Fatalf("invite channel calls = %v, want peerA", got)
	}
	if len(joined.ChannelResults) != 1 || joined.ChannelResults[0].Channel.ID != peerA.Peer.ID {
		t.Fatalf("join channel results = %+v, want peerA result", joined.ChannelResults)
	}

	updated, err := svc.JoinUpdates(ctx, viewerID, joined.Folder.ID, []domain.DialogFolderPeer{peerB}, 102)
	if err != nil {
		t.Fatalf("JoinUpdates peerB: %v", err)
	}
	if got := channels.inviteIDs; len(got) != 2 || got[1] != peerB.Peer.ID {
		t.Fatalf("invite channel calls after updates = %v, want peerB appended", got)
	}
	if len(updated.ChannelResults) != 1 || updated.ChannelResults[0].Channel.ID != peerB.Peer.ID {
		t.Fatalf("update channel results = %+v, want peerB result", updated.ChannelResults)
	}

	leave, err := svc.Leave(ctx, viewerID, joined.Folder.ID, []domain.DialogFolderPeer{peerA}, 103)
	if err != nil {
		t.Fatalf("Leave peerA: %v", err)
	}
	if got := channels.leaveIDs; len(got) != 1 || got[0] != peerA.Peer.ID {
		t.Fatalf("leave channel calls = %v, want peerA", got)
	}
	if len(leave.ChannelResults) != 1 || leave.ChannelResults[0].Channel.ID != peerA.Peer.ID {
		t.Fatalf("leave channel results = %+v, want peerA result", leave.ChannelResults)
	}
}

// 加入、退出、经 chatlist 提交入群申请都把各频道写下的 bot qts 记录汇总到结果里交给 RPC 层
// 推送；入群申请以 ErrInviteRequestSent 结束时，已完成的加入与申请本身的记录都已提交，同样返回。
func TestSharedFolderOperationsReturnEveryBotUpdate(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	const botID = int64(9000)
	peerA := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	peerB := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3002}, AccessHash: 32}
	channels := &fakeChatlistChannels{botUserID: botID, requestChannels: map[int64]bool{peerB.Peer.ID: true}}
	svc := NewService(memory.NewChatlistStore(), dialogs, WithChannels(channels), WithSlugGenerator(func() (string, error) { return "slug-bots", nil }))
	ownerID, viewerID := int64(1001), int64(2002)
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{ID: 2, Title: "Team", IncludePeers: []domain.DialogFolderPeer{peerA, peerB}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peerA, peerB}, 100); err != nil {
		t.Fatal(err)
	}
	qtsOf := func(updates []domain.BotUpdate) []int {
		out := make([]int, 0, len(updates))
		for _, u := range updates {
			if u.BotUserID != botID {
				t.Fatalf("record for %d, want bot %d", u.BotUserID, botID)
			}
			out = append(out, u.Qts)
		}
		return out
	}

	requested, err := svc.JoinInvite(ctx, viewerID, "slug-bots", []domain.DialogFolderPeer{peerA, peerB}, 101)
	if !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("join with a request-only channel err = %v, want ErrInviteRequestSent", err)
	}
	if got := qtsOf(requested.BotUpdates); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("request-sent bot updates = %v, want the join and the request [1 2]", got)
	}

	joined, err := svc.JoinInvite(ctx, viewerID, "slug-bots", []domain.DialogFolderPeer{peerA}, 102)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if got := qtsOf(joined.BotUpdates); len(got) != 1 || got[0] != 3 {
		t.Fatalf("join bot updates = %v, want [3]", got)
	}
	updated, err := svc.JoinUpdates(ctx, viewerID, joined.Folder.ID, []domain.DialogFolderPeer{peerB}, 103)
	if !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("join updates err = %v, want ErrInviteRequestSent", err)
	}
	if got := qtsOf(updated.BotUpdates); len(got) != 1 || got[0] != 4 {
		t.Fatalf("join updates bot updates = %v, want [4]", got)
	}
	left, err := svc.Leave(ctx, viewerID, joined.Folder.ID, []domain.DialogFolderPeer{peerA}, 104)
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	if got := qtsOf(left.BotUpdates); len(got) != 1 || got[0] != 5 {
		t.Fatalf("leave bot updates = %v, want [5]", got)
	}
}

func TestSharedFolderPublicPeerFallsBackToSelfJoin(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	channels := &fakeChatlistChannels{publicOnly: true}
	svc := NewService(
		memory.NewChatlistStore(),
		dialogs,
		WithChannels(channels),
		WithSlugGenerator(func() (string, error) { return "slug-public", nil }),
	)

	ownerID := int64(1001)
	viewerID := int64(2002)
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Public",
		IncludePeers: []domain.DialogFolderPeer{peer},
	}); err != nil {
		t.Fatalf("seed owner folder: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, 100); err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	if _, err := svc.JoinInvite(ctx, viewerID, "slug-public", []domain.DialogFolderPeer{peer}, 101); err != nil {
		t.Fatalf("JoinInvite public peer: %v", err)
	}
	if len(channels.inviteIDs) != 0 {
		t.Fatalf("invite channel calls = %v, want none for public-only owner", channels.inviteIDs)
	}
	if got := channels.joinIDs; len(got) != 1 || got[0] != peer.Peer.ID {
		t.Fatalf("join channel calls = %v, want self-join peer", got)
	}
}

func TestExportInviteRejectsRuleBasedFolder(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	svc := NewService(memory.NewChatlistStore(), dialogs, WithSlugGenerator(func() (string, error) { return "slug-two", nil }))
	ownerID := int64(1001)
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Rule based",
		Groups:       true,
		IncludePeers: []domain.DialogFolderPeer{peer},
	}); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, 0); !errors.Is(err, domain.ErrChatlistNotShareable) {
		t.Fatalf("ExportInvite rule folder err = %v, want ErrChatlistNotShareable", err)
	}
}

func TestExportInviteRejectsUserPeers(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	svc := NewService(memory.NewChatlistStore(), dialogs, WithSlugGenerator(func() (string, error) { return "slug-user-peer", nil }))
	ownerID := int64(1001)
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2002}, AccessHash: 22}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Users",
		IncludePeers: []domain.DialogFolderPeer{peer},
	}); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, 0); !errors.Is(err, domain.ErrChatlistNotShareable) {
		t.Fatalf("ExportInvite user peer err = %v, want ErrChatlistNotShareable", err)
	}
}

func TestChatlistInviteLimitUsesPremiumTier(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	chatlists := memory.NewChatlistStore()
	ownerID := int64(1001)
	peer := domain.DialogFolderPeer{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, AccessHash: 31}
	if err := dialogs.UpsertFolder(ctx, ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IncludePeers: []domain.DialogFolderPeer{peer},
	}); err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	nextSlug := 0
	slugger := func() (string, error) {
		nextSlug++
		return "slug-limit-" + string(rune('a'+nextSlug)), nil
	}
	svc := NewService(chatlists, dialogs, WithSlugGenerator(slugger))
	for i := 0; i < domain.MaxChatlistInvitesDefault; i++ {
		if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, i+1); err != nil {
			t.Fatalf("ExportInvite default #%d: %v", i+1, err)
		}
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, 10); !errors.Is(err, domain.ErrChatlistInvitesTooMuch) {
		t.Fatalf("ExportInvite over default limit err = %v, want ErrChatlistInvitesTooMuch", err)
	}
	if _, err := svc.EditInvite(ctx, ownerID, 2, "slug-limit-b", nil, nil, true); err != nil {
		t.Fatalf("EditInvite revoke first default link: %v", err)
	}
	if _, _, err := svc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, 11); err != nil {
		t.Fatalf("ExportInvite after revoked link freed active limit: %v", err)
	}

	premiumSvc := NewService(
		chatlists,
		dialogs,
		WithSlugGenerator(slugger),
		WithPremiumChecker(func(context.Context, int64) bool { return true }),
	)
	if _, _, err := premiumSvc.ExportInvite(ctx, ownerID, 2, "", []domain.DialogFolderPeer{peer}, 11); err != nil {
		t.Fatalf("ExportInvite premium extra link: %v", err)
	}
}

type fakeChatlistChannels struct {
	getIDs     []int64
	inviteIDs  []int64
	joinIDs    []int64
	leaveIDs   []int64
	publicOnly bool
	// botUserID 非零时每次成员变动都带一条发给它的 qts 记录；requestChannels 里的频道
	// 只接受入群申请（返回带记录的结果与 ErrInviteRequestSent）。
	botUserID       int64
	requestChannels map[int64]bool
	qts             int
}

func (f *fakeChatlistChannels) withBotUpdate(res domain.CreateChannelResult) domain.CreateChannelResult {
	if f.botUserID == 0 {
		return res
	}
	f.qts++
	res.BotUpdates = []domain.BotUpdate{{BotUserID: f.botUserID, Qts: f.qts, BotStopped: &domain.BotStoppedUpdate{UserID: res.Channel.ID}}}
	return res
}

func (f *fakeChatlistChannels) joinOutcome(userID, channelID int64, date int) (domain.CreateChannelResult, error) {
	if f.requestChannels[channelID] {
		return f.withBotUpdate(domain.CreateChannelResult{Channel: domain.Channel{ID: channelID}}), domain.ErrInviteRequestSent
	}
	return f.withBotUpdate(fakeChatlistChannelResult(userID, channelID, domain.ChannelMemberActive, date)), nil
}

func (f *fakeChatlistChannels) GetChannel(_ context.Context, userID, channelID int64) (domain.ChannelView, error) {
	f.getIDs = append(f.getIDs, channelID)
	role := domain.ChannelRoleCreator
	if f.publicOnly {
		role = domain.ChannelRoleMember
	}
	return domain.ChannelView{
		Channel: domain.Channel{
			ID:         channelID,
			AccessHash: channelID * 10,
			Title:      "Team",
			Username:   "team",
			Megagroup:  true,
		},
		Self: domain.ChannelMember{
			ChannelID: channelID,
			UserID:    userID,
			Role:      role,
			Status:    domain.ChannelMemberActive,
		},
	}, nil
}

func (f *fakeChatlistChannels) GetParticipant(context.Context, int64, int64, int64) (domain.ChannelMember, error) {
	return domain.ChannelMember{}, domain.ErrUserNotParticipant
}

func (f *fakeChatlistChannels) InviteToChannel(_ context.Context, userID, channelID int64, userIDs []int64, date int) (domain.CreateChannelResult, error) {
	f.inviteIDs = append(f.inviteIDs, channelID)
	if len(userIDs) == 0 {
		return domain.CreateChannelResult{}, domain.ErrUsersTooMuch
	}
	return f.joinOutcome(userIDs[0], channelID, date)
}

func (f *fakeChatlistChannels) JoinChannel(_ context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error) {
	f.joinIDs = append(f.joinIDs, channelID)
	return f.joinOutcome(userID, channelID, date)
}

func (f *fakeChatlistChannels) LeaveChannel(_ context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error) {
	f.leaveIDs = append(f.leaveIDs, channelID)
	return f.withBotUpdate(fakeChatlistChannelResult(userID, channelID, domain.ChannelMemberLeft, date)), nil
}

func fakeChatlistChannelResult(userID, channelID int64, status domain.ChannelMemberStatus, date int) domain.CreateChannelResult {
	return domain.CreateChannelResult{
		Channel: domain.Channel{
			ID:         channelID,
			AccessHash: channelID * 10,
			Title:      "Team",
			Username:   "team",
			Megagroup:  true,
			Date:       date,
		},
		Members: []domain.ChannelMember{{
			ChannelID: channelID,
			UserID:    userID,
			Role:      domain.ChannelRoleMember,
			Status:    status,
			JoinedAt:  date,
		}},
		Recipients: []int64{userID},
	}
}
