package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) SetChannelGuardBot(ctx context.Context, actor, channel, bot int64, enabled bool) (domain.Channel, error) {
	var ch domain.Channel
	err := withTx(ctx, s.db, "set channel guard bot", func(tx pgx.Tx) error {
		if err := lockChannelInvites(ctx, tx, channel); err != nil {
			return err
		}
		var member domain.ChannelMember
		var err error
		ch, member, err = s.getChannelForMember(ctx, tx, actor, channel)
		if err != nil {
			return err
		}
		if !canChangeChannelInfo(member) {
			return domain.ErrChannelAdminRequired
		}
		if bot != 0 {
			_, m, e := s.getChannelForMember(ctx, tx, bot, channel)
			if e != nil {
				return e
			}
			if !canExportChannelInvite(m) {
				return domain.ErrChannelAdminRequired
			}
		}
		q := sqlcgen.New(tx)
		if err = q.SetChannelGuardJoinRequest(ctx, sqlcgen.SetChannelGuardJoinRequestParams{ID: channel, JoinRequest: enabled}); err != nil {
			return err
		}
		if enabled && bot != 0 {
			err = q.PutChannelGuardBot(ctx, sqlcgen.PutChannelGuardBotParams{ChannelID: channel, BotUserID: bot})
		} else {
			err = q.DeleteChannelGuardBot(ctx, channel)
		}
		ch.JoinRequest = enabled
		if enabled {
			ch.GuardBotID = bot
		} else {
			ch.GuardBotID = 0
		}
		return err
	})
	return ch, err
}
func (s *ChannelStore) GetChannelGuardBot(ctx context.Context, channel int64) (int64, error) {
	bot, err := sqlcgen.New(s.db).GetChannelGuardBot(ctx, channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return bot, nil
}
func joinQueryFromRow(row sqlcgen.ChatJoinQuery) domain.ChatJoinQuery {
	return domain.ChatJoinQuery{ID: row.ID, ChannelID: row.ChannelID, BotUserID: row.BotUserID, UserID: row.UserID, RequestDate: int(row.RequestDate), ExpiresAt: int(row.ExpiresAt), State: row.State, WebAppURL: row.WebAppUrl}
}
func (s *ChannelStore) CreateChatJoinQuery(ctx context.Context, in domain.ChatJoinQuery) (domain.ChatJoinQuery, error) {
	var out domain.ChatJoinQuery
	err := withTx(ctx, s.db, "create chat join query", func(tx pgx.Tx) error {
		if err := lockChannelInvites(ctx, tx, in.ChannelID); err != nil {
			return err
		}
		q := sqlcgen.New(tx)
		bot, err := q.GetChannelGuardBotForUpdate(ctx, in.ChannelID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrChatJoinQueryInvalid
		}
		if err != nil {
			return err
		}
		ch, m, err := s.getChannelForMember(ctx, tx, bot, in.ChannelID)
		if err != nil {
			return err
		}
		if in.ID <= 0 || bot != in.BotUserID || !ch.JoinRequest || !canExportChannelInvite(m) {
			return domain.ErrChatJoinQueryInvalid
		}
		pending, err := s.getPendingInviteImporterTx(ctx, tx, in.ChannelID, in.UserID, true)
		if err != nil {
			return err
		}
		if pending.Date != in.RequestDate {
			return domain.ErrChatJoinQueryInvalid
		}
		row, err := q.InsertChatJoinQuery(ctx, sqlcgen.InsertChatJoinQueryParams{ID: in.ID, ChannelID: in.ChannelID, BotUserID: in.BotUserID, UserID: in.UserID, RequestDate: int32(in.RequestDate), ExpiresAt: int32(in.ExpiresAt)})
		if err == nil {
			out = joinQueryFromRow(row)
		}
		return err
	})
	return out, err
}
func (s *ChannelStore) GetChatJoinQuery(ctx context.Context, id int64) (domain.ChatJoinQuery, error) {
	row, err := sqlcgen.New(s.db).GetChatJoinQuery(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChatJoinQuery{}, domain.ErrChatJoinQueryInvalid
	}
	return joinQueryFromRow(row), err
}
func (s *ChannelStore) ResolveChatJoinQuery(ctx context.Context, bot, id int64, result, url string, now int) (domain.ChatJoinQuery, domain.CreateChannelResult, error) {
	initial, err := s.GetChatJoinQuery(ctx, id)
	if err != nil {
		return domain.ChatJoinQuery{}, domain.CreateChannelResult{}, err
	}
	var query domain.ChatJoinQuery
	var res domain.CreateChannelResult
	err = withTx(ctx, s.db, "resolve chat join query", func(tx pgx.Tx) error {
		if err := lockChannelInvites(ctx, tx, initial.ChannelID); err != nil {
			return err
		}
		q := sqlcgen.New(tx)
		guard, err := q.GetChannelGuardBotForUpdate(ctx, initial.ChannelID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrChatJoinQueryInvalid
		}
		if err != nil {
			return err
		}
		if guard != bot {
			return domain.ErrChatJoinQueryInvalid
		}
		row, err := q.GetChatJoinQueryForUpdate(ctx, id)
		if err != nil {
			return err
		}
		query = joinQueryFromRow(row)
		if !query.CanResolve(bot, now, result) {
			return domain.ErrChatJoinQueryInvalid
		}
		ch, m, err := s.getChannelForMember(ctx, tx, bot, query.ChannelID)
		if err != nil {
			return err
		}
		if !ch.JoinRequest || !canExportChannelInvite(m) {
			return domain.ErrChatJoinQueryInvalid
		}
		pending, err := s.getPendingInviteImporterTx(ctx, tx, query.ChannelID, query.UserID, true)
		if errors.Is(err, domain.ErrHideRequesterMissing) {
			return domain.ErrChatJoinQueryInvalid
		}
		if err != nil {
			return err
		}
		if pending.Date != query.RequestDate {
			return domain.ErrChatJoinQueryInvalid
		}
		res.Channel = ch
		switch result {
		case "webview":
			query.WebAppURL = url
			query.ExpiresAt = now + 300
		case "approve", "decline":
			res, err = s.hideChatJoinRequestTx(ctx, tx, domain.HideChannelJoinRequestRequest{UserID: bot, ChannelID: query.ChannelID, TargetUserID: query.UserID, Approved: result == "approve", Date: now})
			if err != nil {
				return err
			}
		case "queue":
			var invite *domain.ChannelInvite
			if pending.InviteID != 0 {
				value, e := s.getInviteByID(ctx, tx, query.ChannelID, pending.InviteID, false)
				if e != nil {
					return e
				}
				invite = &value
			}
			res.JoinRequest = domain.NewChannelJoinRequest(query.ChannelID, query.UserID, query.RequestDate, invite)
		default:
			return domain.ErrChatJoinQueryResultInvalid
		}
		query.State = result
		return q.UpdateChatJoinQuery(ctx, sqlcgen.UpdateChatJoinQueryParams{ID: id, State: result, WebAppUrl: query.WebAppURL, ExpiresAt: int32(query.ExpiresAt)})
	})
	if err != nil {
		return domain.ChatJoinQuery{}, domain.CreateChannelResult{}, err
	}
	res.Recipients, _ = s.ListActiveChannelMemberIDs(ctx, bot, query.ChannelID, 0)
	return query, res, nil
}

func (s *ChannelStore) ExpireChatJoinQueries(ctx context.Context, now, limit int) ([]domain.ChatJoinQueryOutcome, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := sqlcgen.New(s.db)
	rows, err := q.ListExpiredChatJoinQueries(ctx, sqlcgen.ListExpiredChatJoinQueriesParams{NowDate: int32(now), PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	var out []domain.ChatJoinQueryOutcome
	for _, candidate := range rows {
		var outcome *domain.ChatJoinQueryOutcome
		err = withTx(ctx, s.db, "expire chat join query", func(tx pgx.Tx) error {
			if err := lockChannelInvites(ctx, tx, candidate.ChannelID); err != nil {
				return err
			}
			queries := sqlcgen.New(tx)
			row, err := queries.GetChatJoinQueryForUpdate(ctx, candidate.ID)
			if err != nil {
				return err
			}
			query := joinQueryFromRow(row)
			if query.ExpiresAt > now || (query.State != "pending" && query.State != "webview") {
				return nil
			}
			query.State = "queue"
			if err = queries.UpdateChatJoinQuery(ctx, sqlcgen.UpdateChatJoinQueryParams{ID: query.ID, State: query.State, WebAppUrl: query.WebAppURL, ExpiresAt: int32(query.ExpiresAt)}); err != nil {
				return err
			}
			pending, err := s.getPendingInviteImporterTx(ctx, tx, query.ChannelID, query.UserID, true)
			if errors.Is(err, domain.ErrHideRequesterMissing) {
				return nil
			}
			if err != nil {
				return err
			}
			if pending.Date != query.RequestDate {
				return nil
			}
			channel, err := getChannelByID(ctx, tx, query.ChannelID)
			if errors.Is(err, domain.ErrChannelInvalid) {
				return nil
			}
			if err != nil {
				return err
			}
			var invite *domain.ChannelInvite
			if pending.InviteID != 0 {
				value, e := s.getInviteByID(ctx, tx, query.ChannelID, pending.InviteID, false)
				if e != nil {
					return e
				}
				invite = &value
			}
			outcome = &domain.ChatJoinQueryOutcome{Query: query, Result: domain.CreateChannelResult{Channel: channel, JoinRequest: domain.NewChannelJoinRequest(query.ChannelID, query.UserID, query.RequestDate, invite)}}
			return nil
		})
		if err != nil {
			return out, err
		}
		if outcome != nil {
			out = append(out, *outcome)
		}
	}
	if err = q.DeleteOldChatJoinQueries(ctx, sqlcgen.DeleteOldChatJoinQueriesParams{Cutoff: int32(now - 86400), PageLimit: int32(limit)}); err != nil {
		return out, err
	}
	return out, nil
}
