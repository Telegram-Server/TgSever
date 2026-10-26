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

// botCommandSetColumns 把桶键拆成表的主键列。
type botCommandSetColumns struct {
	scope    string
	peerType string
	peerID   int64
	userID   int64
	lang     string
}

func botCommandSetKeyColumns(key domain.BotCommandSetKey) botCommandSetColumns {
	return botCommandSetColumns{
		scope:    string(key.Scope.Type),
		peerType: string(key.Scope.Peer.Type),
		peerID:   key.Scope.Peer.ID,
		userID:   key.Scope.UserID,
		lang:     key.LangCode,
	}
}

func (s *BotStore) SetBotCommandSet(ctx context.Context, botUserID int64, key domain.BotCommandSetKey, commands []domain.BotCommand) (int, error) {
	if key.IsDefault() {
		return s.UpdateBotCommands(ctx, botUserID, commands)
	}
	if !key.Scope.Valid() || !domain.ValidBotCommandLangCode(key.LangCode) {
		return 0, domain.ErrBotCommandInvalid
	}
	cols := botCommandSetKeyColumns(key)
	var payload []byte
	if len(commands) > 0 {
		var err error
		if payload, err = json.Marshal(commands); err != nil {
			return 0, fmt.Errorf("set bot command set: encode: %w", err)
		}
	}
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.LockBotForCommandSets(ctx, botUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrBotNotFound
			}
			return fmt.Errorf("set bot command set: lock bot: %w", err)
		}
		if len(commands) == 0 {
			if _, err := q.DeleteBotCommandSet(ctx, sqlcgen.DeleteBotCommandSetParams{
				BotUserID: botUserID, Scope: cols.scope, PeerType: cols.peerType,
				PeerID: cols.peerID, UserID: cols.userID, LangCode: cols.lang,
			}); err != nil {
				return fmt.Errorf("set bot command set: delete: %w", err)
			}
			return nil
		}
		_, err := q.GetBotCommandSet(ctx, sqlcgen.GetBotCommandSetParams{
			BotUserID: botUserID, Scope: cols.scope, PeerType: cols.peerType,
			PeerID: cols.peerID, UserID: cols.userID, LangCode: cols.lang,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			count, err := q.CountBotCommandSets(ctx, botUserID)
			if err != nil {
				return fmt.Errorf("set bot command set: count: %w", err)
			}
			if count >= domain.MaxBotCommandSets {
				return domain.ErrBotCommandSetsTooMany
			}
		case err != nil:
			return fmt.Errorf("set bot command set: get: %w", err)
		}
		if err := q.UpsertBotCommandSet(ctx, sqlcgen.UpsertBotCommandSetParams{
			BotUserID: botUserID, Scope: cols.scope, PeerType: cols.peerType,
			PeerID: cols.peerID, UserID: cols.userID, LangCode: cols.lang, Commands: payload,
		}); err != nil {
			return fmt.Errorf("set bot command set: upsert: %w", err)
		}
		return nil
	})
}

func (s *BotStore) GetBotCommandSet(ctx context.Context, botUserID int64, key domain.BotCommandSetKey) ([]domain.BotCommand, bool, error) {
	if key.IsDefault() {
		profile, found, err := s.GetBot(ctx, botUserID)
		if err != nil || !found || len(profile.Commands) == 0 {
			return nil, false, err
		}
		return profile.Commands, true, nil
	}
	cols := botCommandSetKeyColumns(key)
	raw, err := s.q.GetBotCommandSet(ctx, sqlcgen.GetBotCommandSetParams{
		BotUserID: botUserID, Scope: cols.scope, PeerType: cols.peerType,
		PeerID: cols.peerID, UserID: cols.userID, LangCode: cols.lang,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get bot command set: %w", err)
	}
	var commands []domain.BotCommand
	if err := json.Unmarshal(raw, &commands); err != nil {
		return nil, false, fmt.Errorf("get bot command set: decode: %w", err)
	}
	return commands, true, nil
}

func (s *BotStore) ListBotCommandSetsForPeer(ctx context.Context, botUserIDs []int64, peer domain.Peer, viewerUserID int64) ([]domain.BotCommandSet, error) {
	if len(botUserIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListBotCommandSetsForPeer(ctx, sqlcgen.ListBotCommandSetsForPeerParams{
		BotUserIds:   botUserIDs,
		PeerType:     string(peer.Type),
		PeerID:       peer.ID,
		ViewerUserID: viewerUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("list bot command sets: %w", err)
	}
	out := make([]domain.BotCommandSet, 0, len(rows))
	for _, row := range rows {
		set := domain.BotCommandSet{
			BotUserID: row.BotUserID,
			Key: domain.BotCommandSetKey{
				Scope: domain.BotCommandScope{
					Type:   domain.BotCommandScopeType(row.Scope),
					Peer:   domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
					UserID: row.UserID,
				},
				LangCode: row.LangCode,
			},
		}
		if err := json.Unmarshal(row.Commands, &set.Commands); err != nil {
			return nil, fmt.Errorf("list bot command sets: decode: %w", err)
		}
		out = append(out, set)
	}
	return out, nil
}
