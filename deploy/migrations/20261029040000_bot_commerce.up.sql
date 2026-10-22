CREATE TABLE bot_commerce_offers (
 slug text PRIMARY KEY,
 bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 invite_hash text NOT NULL DEFAULT '',
 data jsonb NOT NULL
);
CREATE UNIQUE INDEX bot_commerce_invite_hash ON bot_commerce_offers(invite_hash) WHERE invite_hash <> '';
ALTER TABLE bot_invoices ALTER COLUMN payload TYPE varchar(128);
ALTER TABLE bot_invoices ADD COLUMN link_slug text REFERENCES bot_commerce_offers(slug);
ALTER TABLE bot_invoices DROP CONSTRAINT bot_invoices_shape_check;
ALTER TABLE bot_invoices ADD CONSTRAINT bot_invoices_shape_check CHECK (
 bot_user_id>0 AND chat_id<>0 AND amount>0 AND date>0 AND currency='XTR' AND
 ((message_id>0 AND link_slug IS NULL) OR (message_id=0 AND link_slug IS NOT NULL)) AND
 (NOT paid OR (charge_id<>'' AND payer_user_id>0 AND paid_at>0)) AND (NOT refunded OR paid)
);
DROP INDEX bot_invoices_message_idx;
CREATE UNIQUE INDEX bot_invoices_message_idx ON bot_invoices(bot_user_id,chat_id,message_id) WHERE message_id>0;
CREATE TABLE bot_commerce_checkouts (
 invoice_id bigint PRIMARY KEY REFERENCES bot_invoices(id) ON DELETE CASCADE,
 slug text NOT NULL REFERENCES bot_commerce_offers(slug),
 subscription_id text NOT NULL DEFAULT ''
);
CREATE TABLE bot_commerce_subscriptions (
 id text PRIMARY KEY,
 bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 channel_id bigint NOT NULL DEFAULT 0,
 until_date integer NOT NULL,
 active boolean NOT NULL,
 data jsonb NOT NULL
);
CREATE INDEX bot_commerce_subscriptions_due ON bot_commerce_subscriptions(until_date,id) WHERE active;
CREATE INDEX bot_commerce_subscriptions_user ON bot_commerce_subscriptions(user_id,id);
CREATE TABLE bot_paid_media (
 id text PRIMARY KEY,
 bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 data jsonb NOT NULL
);
CREATE TABLE bot_paid_media_files (
 media_id text NOT NULL REFERENCES bot_paid_media(id) ON DELETE CASCADE,
 location_key text NOT NULL,
 PRIMARY KEY(media_id,location_key)
);
CREATE INDEX bot_paid_media_files_key ON bot_paid_media_files(location_key);
CREATE TABLE bot_paid_media_purchases (
 media_id text NOT NULL REFERENCES bot_paid_media(id) ON DELETE CASCADE,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 amount bigint NOT NULL CHECK(amount>0),
 date integer NOT NULL,
 PRIMARY KEY(media_id,user_id)
);
