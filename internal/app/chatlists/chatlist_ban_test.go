package chatlists

import (
	"context"
	"errors"
	"testing"

	channelsapp "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// chatlistBanFixture 用真实的内存 channel store 搭一个共享文件夹：所有者是两个私有超级群的
// 创建者（有封禁权），文件夹已导出邀请链接 slug。
type chatlistBanFixture struct {
	svc      *Service
	channels *channelsapp.Service
	dialogs  *memory.DialogStore
	ownerID  int64
	viewerID int64
	peerA    domain.DialogFolderPeer
	peerB    domain.DialogFolderPeer
	slug     string
}

func newChatlistBanFixture(t *testing.T, slug string) chatlistBanFixture {
	t.Helper()
	f := chatlistBanFixture{
		channels: channelsapp.NewService(memory.NewChannelStore()),
		dialogs:  memory.NewDialogStore(),
		ownerID:  1001,
		viewerID: 2002,
		slug:     slug,
	}
	f.svc = NewService(
		memory.NewChatlistStore(),
		f.dialogs,
		WithChannels(f.channels),
		WithSlugGenerator(func() (string, error) { return slug, nil }),
	)
	f.peerA = f.createGroup(t, f.ownerID, "Group A")
	f.peerB = f.createGroup(t, f.ownerID, "Group B")
	return f
}

// export 把 peers 放进所有者的文件夹并导出邀请链接 f.slug。
func (f chatlistBanFixture) export(t *testing.T, peers ...domain.DialogFolderPeer) {
	t.Helper()
	ctx := context.Background()
	if err := f.dialogs.UpsertFolder(ctx, f.ownerID, domain.DialogFolder{
		ID:           2,
		Title:        "Team",
		IncludePeers: peers,
	}); err != nil {
		t.Fatalf("seed owner folder: %v", err)
	}
	if _, _, err := f.svc.ExportInvite(ctx, f.ownerID, 2, "", peers, 100); err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
}

func (f chatlistBanFixture) createGroup(t *testing.T, creatorID int64, title string) domain.DialogFolderPeer {
	t.Helper()
	res, err := f.channels.CreateChannel(context.Background(), creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         title,
		Megagroup:     true,
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateChannel %s: %v", title, err)
	}
	return domain.DialogFolderPeer{
		Peer:       domain.Peer{Type: domain.PeerTypeChannel, ID: res.Channel.ID},
		AccessHash: res.Channel.AccessHash,
	}
}

// addViewerThenBan 先由群管理员 adminID 把 viewer 拉进群，再设置 viewer 的封禁权限。
func (f chatlistBanFixture) addViewerThenBan(t *testing.T, adminID int64, peer domain.DialogFolderPeer, rights domain.ChannelBannedRights) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.channels.InviteToChannel(ctx, adminID, peer.Peer.ID, []int64{f.viewerID}, 20); err != nil {
		t.Fatalf("seed viewer membership: %v", err)
	}
	if _, err := f.channels.EditBanned(ctx, adminID, domain.EditChannelBannedRequest{
		UserID:       adminID,
		ChannelID:    peer.Peer.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: f.viewerID},
		BannedRights: rights,
		Date:         30,
	}); err != nil {
		t.Fatalf("EditBanned: %v", err)
	}
}

func (f chatlistBanFixture) viewerMember(t *testing.T, peer domain.DialogFolderPeer) domain.ChannelMember {
	t.Helper()
	member, err := f.channels.GetParticipant(context.Background(), f.ownerID, peer.Peer.ID, f.viewerID)
	if err != nil {
		t.Fatalf("GetParticipant viewer in %d: %v", peer.Peer.ID, err)
	}
	return member
}

func TestChatlistJoinSkipsChannelWhereUserIsKicked(t *testing.T) {
	ctx := context.Background()
	f := newChatlistBanFixture(t, "slug-ban-kicked")
	f.export(t, f.peerA, f.peerB)
	f.addViewerThenBan(t, f.ownerID, f.peerA, domain.ChannelBannedRights{ViewMessages: true})

	joined, err := f.svc.JoinInvite(ctx, f.viewerID, f.slug, nil, 101)
	if err != nil {
		t.Fatalf("JoinInvite: %v", err)
	}

	if got := f.viewerMember(t, f.peerA); got.Status != domain.ChannelMemberKicked || !got.BannedRights.ViewMessages {
		t.Fatalf("kicked group member after chatlist join = %+v, want still kicked", got)
	}
	if got := f.viewerMember(t, f.peerB); got.Status != domain.ChannelMemberActive {
		t.Fatalf("other group member after chatlist join = %+v, want active", got)
	}
	if len(joined.ChannelResults) != 1 || joined.ChannelResults[0].Channel.ID != f.peerB.Peer.ID {
		t.Fatalf("channel results = %+v, want only group B", joined.ChannelResults)
	}
	if peers := joined.Folder.IncludePeers; len(peers) != 1 || peers[0].Peer != f.peerB.Peer {
		t.Fatalf("imported folder peers = %+v, want only group B", peers)
	}
}

// 所有者只是公开群的普通成员（无邀请权）时走 JoinChannel 自加入；被踢者同样只跳过该群。
func TestChatlistJoinSkipsPublicChannelWhereUserIsKicked(t *testing.T) {
	ctx := context.Background()
	f := newChatlistBanFixture(t, "slug-ban-public")
	publicCreator := int64(3003)
	pub := f.createGroup(t, publicCreator, "Public")
	if _, err := f.channels.UpdateUsername(ctx, publicCreator, domain.UpdateChannelUsernameRequest{
		UserID:    publicCreator,
		ChannelID: pub.Peer.ID,
		Username:  "chatlistbanpublic",
	}); err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	if _, err := f.channels.JoinChannel(ctx, f.ownerID, pub.Peer.ID, 15); err != nil {
		t.Fatalf("owner joins public group: %v", err)
	}
	f.export(t, pub, f.peerA)
	f.addViewerThenBan(t, publicCreator, pub, domain.ChannelBannedRights{ViewMessages: true})

	joined, err := f.svc.JoinInvite(ctx, f.viewerID, f.slug, nil, 101)
	if err != nil {
		t.Fatalf("JoinInvite: %v", err)
	}

	if got := f.viewerMember(t, pub); got.Status != domain.ChannelMemberKicked {
		t.Fatalf("public group member after chatlist join = %+v, want still kicked", got)
	}
	if got := f.viewerMember(t, f.peerA); got.Status != domain.ChannelMemberActive {
		t.Fatalf("other group member after chatlist join = %+v, want active", got)
	}
	if len(joined.ChannelResults) != 1 || joined.ChannelResults[0].Channel.ID != f.peerA.Peer.ID {
		t.Fatalf("channel results = %+v, want only group A", joined.ChannelResults)
	}
	if peers := joined.Folder.IncludePeers; len(peers) != 1 || peers[0].Peer != f.peerA.Peer {
		t.Fatalf("imported folder peers = %+v, want only group A", peers)
	}
}

// 所有频道都对该用户封禁时，没有可加入的内容：报 USER_BANNED_IN_CHANNEL，不导入空文件夹。
func TestChatlistJoinWhereEveryChannelBansUserFails(t *testing.T) {
	ctx := context.Background()
	f := newChatlistBanFixture(t, "slug-ban-all")
	f.export(t, f.peerA)
	f.addViewerThenBan(t, f.ownerID, f.peerA, domain.ChannelBannedRights{ViewMessages: true})

	if _, err := f.svc.JoinInvite(ctx, f.viewerID, f.slug, nil, 101); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("JoinInvite err = %v, want ErrChannelUserBanned", err)
	}
	if got := f.viewerMember(t, f.peerA); got.Status != domain.ChannelMemberKicked {
		t.Fatalf("member after failed chatlist join = %+v, want still kicked", got)
	}
	list, err := f.dialogs.ListFolders(ctx, f.viewerID)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	for _, folder := range list.Folders {
		if folder.IsChatlist {
			t.Fatalf("viewer folders = %+v, want no imported chatlist", list.Folders)
		}
	}
}

// 经 chatlist 更新（JoinUpdates）加入新增频道同样不得解封。
func TestChatlistJoinUpdatesSkipsChannelWhereUserIsKicked(t *testing.T) {
	ctx := context.Background()
	f := newChatlistBanFixture(t, "slug-ban-updates")
	f.export(t, f.peerA, f.peerB)
	joined, err := f.svc.JoinInvite(ctx, f.viewerID, f.slug, []domain.DialogFolderPeer{f.peerA}, 101)
	if err != nil {
		t.Fatalf("JoinInvite peerA: %v", err)
	}
	f.addViewerThenBan(t, f.ownerID, f.peerB, domain.ChannelBannedRights{ViewMessages: true})

	if _, err := f.svc.JoinUpdates(ctx, f.viewerID, joined.Folder.ID, []domain.DialogFolderPeer{f.peerB}, 102); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("JoinUpdates err = %v, want ErrChannelUserBanned", err)
	}
	if got := f.viewerMember(t, f.peerB); got.Status != domain.ChannelMemberKicked {
		t.Fatalf("member after chatlist updates = %+v, want still kicked", got)
	}
	folder, found, err := f.dialogs.GetFolder(ctx, f.viewerID, joined.Folder.ID)
	if err != nil || !found || len(folder.IncludePeers) != 1 || folder.IncludePeers[0].Peer != f.peerA.Peer {
		t.Fatalf("local folder = %+v found %v err %v, want only group A", folder, found, err)
	}
}

func TestChatlistJoinKeepsRestrictionsOfRestrictedUser(t *testing.T) {
	ctx := context.Background()
	f := newChatlistBanFixture(t, "slug-ban-restricted")
	f.export(t, f.peerA, f.peerB)
	f.addViewerThenBan(t, f.ownerID, f.peerA, domain.ChannelBannedRights{SendMessages: true, SendMedia: true})
	if _, err := f.channels.LeaveChannel(ctx, f.viewerID, f.peerA.Peer.ID, 40); err != nil {
		t.Fatalf("viewer leaves restricted group: %v", err)
	}

	joined, err := f.svc.JoinInvite(ctx, f.viewerID, f.slug, nil, 101)
	if err != nil {
		t.Fatalf("JoinInvite: %v", err)
	}

	got := f.viewerMember(t, f.peerA)
	if got.Status != domain.ChannelMemberActive || !got.BannedRights.SendMessages || !got.BannedRights.SendMedia {
		t.Fatalf("restricted member after chatlist join = %+v, want active with restrictions kept", got)
	}
	if len(joined.ChannelResults) != 2 || len(joined.Folder.IncludePeers) != 2 {
		t.Fatalf("join = results %+v folder %+v, want both groups", joined.ChannelResults, joined.Folder.IncludePeers)
	}
}
