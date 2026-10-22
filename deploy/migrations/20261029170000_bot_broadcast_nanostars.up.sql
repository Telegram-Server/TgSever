-- Extend the existing revenue wallet and journal; whole-Star clients keep
-- their exact integer representation, while fractional fees carry signed nanos.
ALTER TABLE bot_stars_balances ADD COLUMN nanos integer NOT NULL DEFAULT 0
 CHECK (nanos >= 0 AND nanos < 1000000000);
ALTER TABLE bot_stars_transactions ADD COLUMN nanos integer NOT NULL DEFAULT 0
 CHECK (nanos > -1000000000 AND nanos < 1000000000);
ALTER TABLE bot_stars_transactions DROP CONSTRAINT bot_stars_transactions_shape_check;
ALTER TABLE bot_stars_transactions ADD CONSTRAINT bot_stars_transactions_shape_check CHECK (
 bot_user_id>0 AND actor_user_id>0 AND (amount<>0 OR nanos<>0) AND date>0 AND
 (amount=0 OR nanos=0 OR (amount>0 AND nanos>0) OR (amount<0 AND nanos<0)) AND
 ((peer_type='' AND peer_id=0) OR (peer_type IN ('user','channel') AND peer_id>0))
);
CREATE TABLE bot_paid_broadcast_settings (
 bot_user_id bigint PRIMARY KEY REFERENCES bots(bot_user_id) ON DELETE CASCADE,
 enabled boolean NOT NULL DEFAULT false
);
CREATE TABLE bot_broadcast_reservations (
 -- Like the wallet journal, receipts survive bot deletion so pending funds
 -- can still be released and money history cannot disappear by cascade.
 bot_user_id bigint NOT NULL CHECK (bot_user_id>0),
 request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
 state text NOT NULL CHECK (state IN ('reserved','committed','released')),
 created_at timestamptz NOT NULL,
 completed_at timestamptz,
 data jsonb NOT NULL,
 PRIMARY KEY (bot_user_id,request_key)
);
CREATE INDEX bot_broadcast_active_idx ON bot_broadcast_reservations(bot_user_id,state,completed_at);
