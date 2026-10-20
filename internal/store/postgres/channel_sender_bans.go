package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) GetChannelSenderBan(ctx context.Context, viewerUserID, channelID, senderChannelID int64) (domain.ChannelMember, error) {
	_, viewer, _, err := s.getChannelForViewer(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelMember{}, err
	}
	if !isChannelAdmin(viewer) {
		return domain.ChannelMember{}, domain.ErrChannelAdminRequired
	}
	row, err := sqlcgen.New(s.db).GetChannelSenderBanIdentity(ctx, sqlcgen.GetChannelSenderBanIdentityParams{ChannelID: channelID, SenderChannelID: senderChannelID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelMember{}, domain.ErrUserNotParticipant
	}
	if err != nil {
		return domain.ChannelMember{}, err
	}
	member := domain.ChannelSenderBanMember(channelID, senderChannelID, row.BannedBy, int(row.BannedAt))
	member.SenderChannelIdentity = domain.ChannelSenderIdentity{Title: row.Title, AccessHash: row.AccessHash, Broadcast: row.Broadcast, Megagroup: row.Megagroup}
	return member, nil
}

func (s *ChannelStore) editChannelSenderBan(ctx context.Context, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error) {
	if err := domain.ValidateChannelSenderBan(req); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelBannedResult{}, fmt.Errorf("edit sender ban: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	defer tx.Rollback(ctx)
	channel, actor, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	if !canBanChannelUsers(actor) {
		return domain.EditChannelBannedResult{}, domain.ErrChannelAdminRequired
	}
	sender, err := s.channelByID(ctx, tx, req.Participant.ID)
	if err != nil || sender.Deleted {
		return domain.EditChannelBannedResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	q := sqlcgen.New(tx)
	previous := domain.ChannelMember{ChannelID: req.ChannelID, Peer: req.Participant, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft}
	row, err := q.GetChannelSenderBan(ctx, sqlcgen.GetChannelSenderBanParams{ChannelID: req.ChannelID, SenderChannelID: req.Participant.ID})
	if err == nil {
		previous = domain.ChannelSenderBanMember(req.ChannelID, req.Participant.ID, row.BannedBy, int(row.BannedAt))
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.EditChannelBannedResult{}, err
	}
	if previous.Status != domain.ChannelMemberKicked && !req.BannedRights.ViewMessages {
		return domain.EditChannelBannedResult{NoChange: true, Channel: channel, Previous: previous, Participant: previous, Date: req.Date}, nil
	}
	member := previous
	member.InviterUserID = req.UserID
	member.LeftAt = req.Date
	if req.BannedRights.ViewMessages {
		member = domain.ChannelSenderBanMember(req.ChannelID, req.Participant.ID, req.UserID, req.Date)
		err = q.PutChannelSenderBan(ctx, sqlcgen.PutChannelSenderBanParams{ChannelID: req.ChannelID, SenderChannelID: req.Participant.ID, BannedBy: req.UserID, BannedAt: int32(req.Date)})
	} else {
		var deleted int64
		deleted, err = q.DeleteChannelSenderBan(ctx, sqlcgen.DeleteChannelSenderBanParams{ChannelID: req.ChannelID, SenderChannelID: req.Participant.ID})
		if err == nil && deleted == 0 {
			member.Status = domain.ChannelMemberLeft
			member.BannedRights = domain.ChannelBannedRights{}
			return domain.EditChannelBannedResult{NoChange: true, Channel: channel, Previous: member, Participant: member, Date: req.Date}, nil
		}
		member.Status = domain.ChannelMemberLeft
		member.BannedRights = domain.ChannelBannedRights{}
	}
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{ChannelID: req.ChannelID, UserID: req.UserID, Date: req.Date, Type: adminLogBanType(previous, member), PrevParticipant: &previous, NewParticipant: &member}); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	recipients, err := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	return domain.EditChannelBannedResult{Channel: channel, Previous: previous, Participant: member, Recipients: recipients, Date: req.Date}, nil
}

func (s *ChannelStore) listChannelKickedPeers(ctx context.Context, channel domain.Channel, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	rows, err := sqlcgen.New(s.db).ListChannelKickedPeers(ctx, sqlcgen.ListChannelKickedPeersParams{ChannelID: channel.ID, NowDate: int32(nowUnix()), Query: filter.Query, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	out := domain.ChannelParticipantList{Channel: channel}
	for _, row := range rows {
		member := domain.ChannelMember{ChannelID: channel.ID, UserID: row.PeerID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberKicked, InviterUserID: row.ActorID, LeftAt: int(row.BanDate), JoinedAt: int(row.JoinedAt), Rank: row.Rank}
		if err := json.Unmarshal(row.BannedRights, &member.BannedRights); err != nil {
			return domain.ChannelParticipantList{}, err
		}
		if row.SenderChannel {
			member.UserID = 0
			member.Peer = domain.Peer{Type: domain.PeerTypeChannel, ID: row.PeerID}
			member.SenderChannelIdentity = domain.ChannelSenderIdentity{Title: row.SenderTitle, AccessHash: row.SenderAccessHash, Broadcast: row.SenderBroadcast, Megagroup: row.SenderMegagroup}
		}
		out.Participants = append(out.Participants, member)
		out.Count = int(row.TotalCount)
	}
	if len(rows) == 0 {
		count, err := sqlcgen.New(s.db).CountChannelKickedPeers(ctx, sqlcgen.CountChannelKickedPeersParams{ChannelID: channel.ID, NowDate: int32(nowUnix()), Query: filter.Query})
		if err != nil {
			return domain.ChannelParticipantList{}, err
		}
		out.Count = int(count)
	}
	return out, nil
}
