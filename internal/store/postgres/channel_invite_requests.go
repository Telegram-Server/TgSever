package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) ListInviteImporters(ctx context.Context, req domain.ChannelInviteImportersRequest) (domain.ChannelInviteImporterList, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelInviteImporterList{}, domain.ErrChannelInvalid
	}
	_, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelInviteImporterList{}, err
	}
	if !canExportChannelInvite(member) {
		return domain.ChannelInviteImporterList{}, domain.ErrChannelAdminRequired
	}
	var inviteID int64
	if req.Hash != "" {
		invite, err := s.getInviteByChannelHash(ctx, s.db, req.ChannelID, req.Hash, false)
		if err != nil {
			return domain.ChannelInviteImporterList{}, err
		}
		if !canManageChannelInvite(member, invite.AdminUserID) {
			return domain.ChannelInviteImporterList{}, domain.ErrChannelAdminRequired
		}
		inviteID = invite.InviteID
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelInviteListLimit {
		limit = domain.MaxChannelInviteListLimit
	}
	ownerFilter := int64(0)
	if !req.Requested && member.Role != domain.ChannelRoleCreator {
		ownerFilter = req.UserID
	}
	q := sqlcgen.New(s.db)
	total, err := q.CountChannelInviteImporters(ctx, sqlcgen.CountChannelInviteImportersParams{
		ChannelID: req.ChannelID, Requested: req.Requested, InviteID: inviteID,
		Query: req.Query, OffsetDate: int32(req.OffsetDate), OffsetUserID: req.OffsetUserID,
		OwnerFilter: ownerFilter,
	})
	if err != nil {
		return domain.ChannelInviteImporterList{}, err
	}
	rows, err := q.ListChannelInviteImporters(ctx, sqlcgen.ListChannelInviteImportersParams{
		ChannelID: req.ChannelID, Requested: req.Requested, InviteID: inviteID,
		Query: req.Query, OffsetDate: int32(req.OffsetDate), OffsetUserID: req.OffsetUserID,
		OwnerFilter: ownerFilter, PageLimit: int32(limit),
	})
	if err != nil {
		return domain.ChannelInviteImporterList{}, err
	}
	importers := make([]domain.ChannelInviteImporter, 0, len(rows))
	for _, row := range rows {
		importers = append(importers, domain.ChannelInviteImporter{
			ChannelID: row.ChannelID, InviteID: row.InviteID, UserID: row.UserID, Date: int(row.Date),
			Requested: row.Requested, ApprovedBy: row.ApprovedBy, ViaChatlist: row.ViaChatlist, About: row.About,
		})
	}
	return domain.ChannelInviteImporterList{Count: int(total), Importers: importers}, nil
}

func (s *ChannelStore) PendingJoinRequests(ctx context.Context, channelID int64, limit int) (domain.ChannelPendingJoinRequests, error) {
	if channelID == 0 {
		return domain.ChannelPendingJoinRequests{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxChannelPendingJoinRecentRequesters {
		limit = domain.MaxChannelPendingJoinRecentRequesters
	}
	rows, err := s.db.Query(ctx, `
SELECT user_id, COUNT(*) OVER()::int
FROM channel_invite_importers
WHERE channel_id = $1 AND requested
ORDER BY date DESC, user_id DESC
LIMIT $2`, channelID, limit)
	if err != nil {
		return domain.ChannelPendingJoinRequests{}, fmt.Errorf("list pending channel join requests: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelPendingJoinRequests{
		ChannelID:        channelID,
		RecentRequesters: make([]int64, 0, limit),
	}
	for rows.Next() {
		var userID int64
		var count int
		if err := rows.Scan(&userID, &count); err != nil {
			return domain.ChannelPendingJoinRequests{}, err
		}
		out.Count = count
		out.RecentRequesters = append(out.RecentRequesters, userID)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelPendingJoinRequests{}, err
	}
	return out, nil
}

func (s *ChannelStore) HideAllChatJoinRequests(ctx context.Context, req domain.HideChannelJoinRequestsRequest) (domain.CreateChannelResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	var result domain.CreateChannelResult
	err := withTx(ctx, s.db, "hide channel join requests", func(tx pgx.Tx) error {
		channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
		if err != nil {
			return err
		}
		if !canExportChannelInvite(member) {
			return domain.ErrChannelAdminRequired
		}
		var inviteID int64
		if req.Hash != "" {
			invite, err := s.getInviteByChannelHash(ctx, tx, req.ChannelID, req.Hash, true)
			if err != nil {
				return err
			}
			if !canManageChannelInvite(member, invite.AdminUserID) {
				return domain.ErrChannelAdminRequired
			}
			inviteID = invite.InviteID
		}
		limit := req.Limit
		if limit <= 0 || limit > domain.MaxChannelHideJoinRequests {
			limit = domain.MaxChannelHideJoinRequests
		}
		targets, err := sqlcgen.New(tx).ListPendingJoinRequestTargetsForUpdate(ctx, sqlcgen.ListPendingJoinRequestTargetsForUpdateParams{ChannelID: req.ChannelID, InviteID: inviteID, Limit: int32(limit)})
		if err != nil {
			return err
		}
		result.Channel = channel
		for _, target := range targets {
			next, err := s.hideChatJoinRequestTx(ctx, tx, domain.HideChannelJoinRequestRequest{
				UserID:       req.UserID,
				ChannelID:    req.ChannelID,
				TargetUserID: target,
				Approved:     req.Approved,
				Date:         req.Date,
			})
			if err != nil {
				return err
			}
			result = result.MergeJoined(next)
		}
		return nil
	})
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	result.Recipients, _ = s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return result, nil
}

func (s *ChannelStore) ListChannelInviteAdminMemberIDs(ctx context.Context, channelID int64, limit int) ([]int64, error) {
	if channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxChannelRealtimeFanout {
		limit = domain.MaxChannelRealtimeFanout
	}
	ids, err := sqlcgen.New(s.db).ListChannelInviteAdminMemberIDs(ctx, sqlcgen.ListChannelInviteAdminMemberIDsParams{
		ChannelID: channelID, Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list channel invite admin members: %w", err)
	}
	return ids, nil
}
