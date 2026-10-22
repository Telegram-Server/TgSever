-- name: PutCommerceOffer :exec
INSERT INTO bot_commerce_offers(slug,bot_id,invite_hash,data) VALUES(sqlc.arg(slug)::text,sqlc.arg(bot_id)::bigint,sqlc.arg(invite_hash)::text,sqlc.arg(data)::jsonb);
-- name: GetCommerceOffer :one
SELECT data FROM bot_commerce_offers WHERE slug=sqlc.arg(slug)::text;
-- name: GetCommerceInviteOffer :one
SELECT data FROM bot_commerce_offers WHERE invite_hash=sqlc.arg(invite_hash)::text;
-- name: PutCommerceCheckout :exec
INSERT INTO bot_commerce_checkouts(invoice_id,slug,subscription_id) VALUES(sqlc.arg(invoice_id)::bigint,sqlc.arg(slug)::text,sqlc.arg(subscription_id)::text);
-- name: GetCommerceCheckout :one
SELECT slug,subscription_id FROM bot_commerce_checkouts WHERE invoice_id=sqlc.arg(invoice_id)::bigint;
-- name: GetCommerceInvoice :one
SELECT jsonb_build_object('ID',i.id,'BotUserID',i.bot_user_id,'ChatID',i.chat_id,'MessageID',i.message_id,'Title',i.title,'Description',i.description,'Amount',i.amount,'Currency',i.currency,'Payload',i.payload,'ChargeID',i.charge_id,'PayerID',i.payer_user_id,'Paid',i.paid,'Refunded',i.refunded,'PaidAt',i.paid_at,'Date',i.date)::jsonb AS data FROM bot_invoices i JOIN bot_commerce_checkouts c ON c.invoice_id=i.id WHERE i.id=sqlc.arg(invoice_id)::bigint AND i.chat_id=sqlc.arg(user_id)::bigint AND c.slug=sqlc.arg(slug)::text;
-- name: PutCommerceSubscription :exec
INSERT INTO bot_commerce_subscriptions(id,bot_id,user_id,channel_id,until_date,active,data) VALUES(sqlc.arg(id)::text,sqlc.arg(bot_id)::bigint,sqlc.arg(user_id)::bigint,sqlc.arg(channel_id)::bigint,sqlc.arg(until_date)::integer,sqlc.arg(active)::boolean,sqlc.arg(data)::jsonb)
ON CONFLICT(id) DO UPDATE SET until_date=EXCLUDED.until_date,active=EXCLUDED.active,data=EXCLUDED.data;
-- name: LockCommerceSubscription :one
SELECT data FROM bot_commerce_subscriptions WHERE id=sqlc.arg(id)::text FOR UPDATE;
-- name: FindCommerceSubscription :one
SELECT data FROM bot_commerce_subscriptions WHERE bot_id=sqlc.arg(bot_id)::bigint AND user_id=sqlc.arg(user_id)::bigint AND data->>'ChargeID'=sqlc.arg(charge_id)::text FOR UPDATE;
-- name: ListCommerceUserSubscriptions :many
SELECT data FROM bot_commerce_subscriptions WHERE user_id=sqlc.arg(user_id)::bigint ORDER BY id;
-- name: ListDueCommerceSubscriptions :many
SELECT id FROM bot_commerce_subscriptions WHERE active AND until_date<=sqlc.arg(date)::integer ORDER BY until_date,id LIMIT sqlc.arg(max_rows)::integer;
-- name: HasCommerceChannelSubscription :one
SELECT EXISTS(SELECT 1 FROM bot_commerce_subscriptions WHERE channel_id=sqlc.arg(channel_id)::bigint AND user_id=sqlc.arg(user_id)::bigint AND active AND until_date>sqlc.arg(date)::integer);
-- name: PutPaidMedia :exec
INSERT INTO bot_paid_media(id,bot_id,owner_id,data) VALUES(sqlc.arg(id)::text,sqlc.arg(bot_id)::bigint,sqlc.arg(owner_id)::bigint,sqlc.arg(data)::jsonb);
-- name: GetPaidMedia :one
SELECT data FROM bot_paid_media WHERE id=sqlc.arg(id)::text;
-- name: LockPaidMedia :one
SELECT data FROM bot_paid_media WHERE id=sqlc.arg(id)::text FOR UPDATE;
-- name: PutPaidMediaFile :exec
INSERT INTO bot_paid_media_files(media_id,location_key) VALUES(sqlc.arg(media_id)::text,sqlc.arg(location_key)::text) ON CONFLICT DO NOTHING;
-- name: HasPaidMediaPurchase :one
SELECT EXISTS(SELECT 1 FROM bot_paid_media_purchases WHERE media_id=sqlc.arg(media_id)::text AND user_id=sqlc.arg(user_id)::bigint);
-- name: PutPaidMediaPurchase :exec
INSERT INTO bot_paid_media_purchases(media_id,user_id,amount,date) VALUES(sqlc.arg(media_id)::text,sqlc.arg(user_id)::bigint,sqlc.arg(amount)::bigint,sqlc.arg(date)::integer);
-- name: CanReadPaidMediaFile :one
SELECT NOT EXISTS(SELECT 1 FROM bot_paid_media_files WHERE location_key=sqlc.arg(location_key)::text) OR EXISTS(SELECT 1 FROM bot_paid_media_files f JOIN bot_paid_media m ON m.id=f.media_id WHERE f.location_key=sqlc.arg(location_key)::text AND (m.bot_id=sqlc.arg(user_id)::bigint OR m.owner_id=sqlc.arg(user_id)::bigint OR EXISTS(SELECT 1 FROM bot_paid_media_purchases p WHERE p.media_id=m.id AND p.user_id=sqlc.arg(user_id)::bigint)));
-- name: CommerceWalletTransactions :many
SELECT jsonb_build_object('ID',CASE WHEN t.reason='bot_refund' THEN regexp_replace(t.invoice_key,'-refund$','') ELSE t.invoice_key END,'Amount',t.amount,'Nanos',t.nanos,'Date',t.date,'Peer',jsonb_build_object('Type',COALESCE(t.peer_type,''),'ID',COALESCE(t.peer_id,0)),'Reason',t.reason,'Payload',COALESCE(i.payload,pm.data->>'Payload',''),'SubscriptionPeriod',COALESCE((o.data->>'SubscriptionPeriod')::integer,0))::jsonb AS data
FROM bot_stars_transactions t LEFT JOIN bot_invoices i ON i.charge_id=regexp_replace(t.invoice_key,'-refund$','') LEFT JOIN bot_commerce_checkouts c ON c.invoice_id=i.id LEFT JOIN bot_commerce_offers o ON o.slug=c.slug LEFT JOIN bot_paid_media pm ON pm.id=split_part(substring(t.invoice_key FROM 11),':',1) AND t.reason='paid_media'
WHERE t.bot_user_id=sqlc.arg(bot_id)::bigint ORDER BY t.id DESC LIMIT sqlc.arg(max_rows)::integer OFFSET sqlc.arg(skip_rows)::integer;
-- name: CommerceCreditChannelBalance :exec
INSERT INTO channel_stars_balances(channel_id,balance) VALUES(sqlc.arg(channel_id)::bigint,sqlc.arg(amount)::bigint) ON CONFLICT(channel_id) DO UPDATE SET balance=channel_stars_balances.balance+EXCLUDED.balance,updated_at=now();
-- name: CommerceChannelTransaction :exec
INSERT INTO channel_stars_transactions(channel_id,actor_user_id,amount,reason,peer_type,peer_id,gift_id,date) VALUES(sqlc.arg(channel_id)::bigint,sqlc.arg(user_id)::bigint,sqlc.arg(amount)::bigint,sqlc.arg(reason)::text,'user',sqlc.arg(user_id)::bigint,0,sqlc.arg(date)::integer);
-- name: CommerceCreditBotBalance :exec
INSERT INTO bot_stars_balances(bot_user_id,balance) VALUES(sqlc.arg(bot_id)::bigint,sqlc.arg(amount)::bigint) ON CONFLICT(bot_user_id) DO UPDATE SET balance=bot_stars_balances.balance+EXCLUDED.balance,updated_at=now();
-- name: CommerceBotTransaction :exec
INSERT INTO bot_stars_transactions(bot_user_id,actor_user_id,amount,reason,peer_type,peer_id,invoice_key,date) VALUES(sqlc.arg(bot_id)::bigint,sqlc.arg(user_id)::bigint,sqlc.arg(amount)::bigint,sqlc.arg(reason)::text,'user',sqlc.arg(user_id)::bigint,sqlc.arg(invoice_key)::text,sqlc.arg(date)::integer);
-- name: GetCommerceSubscription :one
SELECT data FROM bot_commerce_subscriptions WHERE id=sqlc.arg(id)::text;
-- name: CreateCommerceLinkInvoice :one
INSERT INTO bot_invoices(bot_user_id,chat_id,message_id,title,description,amount,currency,payload,date,link_slug)
VALUES(sqlc.arg(bot_id)::bigint,sqlc.arg(user_id)::bigint,0,sqlc.arg(title)::text,sqlc.arg(description)::text,sqlc.arg(amount)::bigint,'XTR',sqlc.arg(payload)::text,sqlc.arg(date)::integer,sqlc.arg(slug)::text)
RETURNING id;
