package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"strconv"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"time"
)

// MessageDraftStore shares 30-second generation state across RPC instances.
type MessageDraftStore struct{ c redis.UniversalClient }

func NewMessageDraftStore(c redis.UniversalClient) *MessageDraftStore {
	return &MessageDraftStore{c: c}
}
func messageDraftRedisKey(botID, userID int64, threadID int) string {
	return fmt.Sprintf("telesrv:{message-draft:%d:%d:%d}:active", botID, userID, threadID)
}
func messageDraftStoppedKey(key string, id int64) string {
	return key + ":stopped:" + strconv.FormatInt(id, 10)
}

var replaceMessageDraftScript = redis.NewScript(`
local stopped_until=redis.call('HGET',KEYS[2],'expires')
if stopped_until and tonumber(stopped_until)>tonumber(ARGV[6]) then return 1 end
redis.call('DEL',KEYS[2])
redis.call('HSET',KEYS[1],'id',ARGV[1],'can_stop',ARGV[2],'body',ARGV[3],'expires',ARGV[5])
redis.call('PEXPIRE',KEYS[1],ARGV[4])
return 0
`)

func (s *MessageDraftStore) ReplaceMessageDraft(ctx context.Context, draft domain.MessageDraft, now time.Time) error {
	if err := draft.Validate(now); err != nil {
		return err
	}
	if s == nil || s.c == nil {
		return errors.New("message draft Redis store is not configured")
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return err
	}
	key := messageDraftRedisKey(draft.BotUserID, draft.UserID, draft.ThreadID)
	canStop := "0"
	if draft.CanStop {
		canStop = "1"
	}
	status, err := replaceMessageDraftScript.Run(ctx, s.c, []string{key, messageDraftStoppedKey(key, draft.ID)}, strconv.FormatInt(draft.ID, 10), canStop, raw, draft.ExpiresAt.Sub(now).Milliseconds(), draft.ExpiresAt.UnixMilli(), now.UnixMilli()).Int()
	if err != nil {
		return fmt.Errorf("redis replace message draft: %w", err)
	}
	if status == 1 {
		return domain.ErrMessageDraftStopped
	}
	return nil
}

var stopMessageDraftScript = redis.NewScript(`
local stopped_until=redis.call('HGET',KEYS[2],'expires')
if stopped_until and tonumber(stopped_until)>tonumber(ARGV[3]) then
 if redis.call('HGET',KEYS[2],'kind') == 'finished' then return {2,''} end
 return {1,''}
end
redis.call('DEL',KEYS[2])
local id=redis.call('HGET',KEYS[1],'id')
if not id or id ~= ARGV[1] then return {2,''} end
local expires=redis.call('HGET',KEYS[1],'expires')
if not expires or tonumber(expires)<=tonumber(ARGV[3]) then
 redis.call('DEL',KEYS[1])
 return {2,''}
end
if redis.call('HGET',KEYS[1],'can_stop') ~= '1' then return {3,''} end
local body=redis.call('HGET',KEYS[1],'body')
redis.call('HSET',KEYS[2],'kind','stopped','expires',ARGV[4])
redis.call('PEXPIRE',KEYS[2],ARGV[2])
redis.call('DEL',KEYS[1])
return {0,body}
`)

func (s *MessageDraftStore) StopMessageDraft(ctx context.Context, botID, userID int64, threadID int, draftID int64, now time.Time) (domain.MessageDraft, bool, error) {
	if s == nil || s.c == nil {
		return domain.MessageDraft{}, false, errors.New("message draft Redis store is not configured")
	}
	key := messageDraftRedisKey(botID, userID, threadID)
	result, err := stopMessageDraftScript.Run(ctx, s.c, []string{key, messageDraftStoppedKey(key, draftID)}, strconv.FormatInt(draftID, 10), domain.MessageDraftLifetime.Milliseconds(), now.UnixMilli(), now.Add(domain.MessageDraftLifetime).UnixMilli()).Result()
	if err != nil {
		return domain.MessageDraft{}, false, fmt.Errorf("redis stop message draft: %w", err)
	}
	status, raw, err := decodeEphemeralScriptResult(result)
	if err != nil {
		return domain.MessageDraft{}, false, err
	}
	switch status {
	case 1:
		return domain.MessageDraft{}, false, nil
	case 2:
		return domain.MessageDraft{}, false, domain.ErrMessageDraftExpired
	case 3:
		return domain.MessageDraft{}, false, domain.ErrMessageDraftStopForbidden
	case 0:
		var draft domain.MessageDraft
		if err := json.Unmarshal(raw, &draft); err != nil {
			return domain.MessageDraft{}, false, err
		}
		if draft.BotUserID != botID || draft.UserID != userID || draft.ThreadID != threadID || draft.ID != draftID {
			return domain.MessageDraft{}, false, domain.ErrMessageDraftInvalid
		}
		if !now.Before(draft.ExpiresAt) {
			return domain.MessageDraft{}, false, domain.ErrMessageDraftExpired
		}
		return draft, true, nil
	default:
		return domain.MessageDraft{}, false, domain.ErrMessageDraftInvalid
	}
}

var finishMessageDraftScript = redis.NewScript(`
local id=redis.call('HGET',KEYS[1],'id')
if not id then return 0 end
local expires=redis.call('HGET',KEYS[1],'expires')
if expires and tonumber(expires)>tonumber(ARGV[2]) then
 redis.call('HSET',KEYS[1] .. ':stopped:' .. id,'kind','finished','expires',ARGV[3])
 redis.call('PEXPIRE',KEYS[1] .. ':stopped:' .. id,ARGV[1])
end
redis.call('DEL',KEYS[1])
return 1
`)

func (s *MessageDraftStore) FinishMessageDraft(ctx context.Context, botID, userID int64, threadID int, now time.Time) error {
	if s == nil || s.c == nil {
		return errors.New("message draft Redis store is not configured")
	}
	err := finishMessageDraftScript.Run(ctx, s.c, []string{messageDraftRedisKey(botID, userID, threadID)}, domain.MessageDraftLifetime.Milliseconds(), now.UnixMilli(), now.Add(domain.MessageDraftLifetime).UnixMilli()).Err()
	if err != nil {
		return fmt.Errorf("redis finish message draft: %w", err)
	}
	return nil
}

var _ store.MessageDraftStore = (*MessageDraftStore)(nil)
