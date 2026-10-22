-- name: CreateBotGame :exec
INSERT INTO bot_games(id,bot_user_id,short_name,game) VALUES ($1,$2,$3,$4);
-- name: GetBotGame :one
SELECT game FROM bot_games WHERE bot_user_id=$1 AND short_name=$2;
-- name: GetBotGameByID :one
SELECT game FROM bot_games WHERE id=$1;
-- name: SetBotGameScore :execrows
INSERT INTO bot_game_scores(game_id,chat_id,inline_owner_id,inline_message_id,user_id,score)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (game_id,chat_id,inline_owner_id,inline_message_id,user_id)
DO UPDATE SET score=EXCLUDED.score
WHERE sqlc.arg(force)::boolean OR bot_game_scores.score<EXCLUDED.score;
-- name: BotGameScores :many
SELECT user_id,score FROM bot_game_scores
WHERE game_id=$1 AND chat_id=$2 AND inline_owner_id=$3 AND inline_message_id=$4
ORDER BY score DESC,user_id ASC;
-- name: EnsurePassportData :exec
INSERT INTO passport_user_data(user_id) VALUES ($1) ON CONFLICT DO NOTHING;
-- name: LockPassportData :one
SELECT data FROM passport_user_data WHERE user_id=$1 FOR UPDATE;
-- name: GetPassportData :one
SELECT data FROM passport_user_data WHERE user_id=$1;
-- name: PutPassportData :exec
UPDATE passport_user_data SET data=$2 WHERE user_id=$1;
-- name: SetPassportPublicKey :exec
INSERT INTO bot_passport_public_keys(bot_user_id,public_key) VALUES ($1,$2)
ON CONFLICT(bot_user_id) DO UPDATE SET public_key=EXCLUDED.public_key;
-- name: PassportPublicKey :one
SELECT public_key FROM bot_passport_public_keys WHERE bot_user_id=$1;
-- name: PutBusinessGameMessageRef :exec
INSERT INTO bot_business_game_messages(bot_user_id,chat_id,message_id,owner_user_id,connection_id)
VALUES ($1,$2,$3,$4,$5);
-- name: BusinessGameMessageRefs :many
SELECT owner_user_id,connection_id FROM bot_business_game_messages
WHERE bot_user_id=$1 AND chat_id=$2 AND message_id=$3;
