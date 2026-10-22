-- An integer-only deployment cannot represent fractional money. Refuse a
-- lossy rollback rather than silently truncating balances or pending funds.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM bot_stars_balances WHERE nanos<>0)
 OR EXISTS(SELECT 1 FROM bot_stars_transactions WHERE nanos<>0)
 OR EXISTS(SELECT 1 FROM bot_broadcast_reservations WHERE state='reserved') THEN
  RAISE EXCEPTION 'cannot downgrade nanostar ledger with fractional or reserved funds';
 END IF;
END $$;
DROP TABLE bot_broadcast_reservations;
DROP TABLE bot_paid_broadcast_settings;
ALTER TABLE bot_stars_transactions DROP CONSTRAINT bot_stars_transactions_shape_check;
ALTER TABLE bot_stars_transactions DROP COLUMN nanos;
ALTER TABLE bot_stars_transactions ADD CONSTRAINT bot_stars_transactions_shape_check CHECK (
 bot_user_id>0 AND actor_user_id>0 AND amount<>0 AND date>0 AND
 ((peer_type='' AND peer_id=0) OR (peer_type IN ('user','channel') AND peer_id>0))
);
ALTER TABLE bot_stars_balances DROP COLUMN nanos;
