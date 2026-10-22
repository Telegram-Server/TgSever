DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM bot_invoices WHERE (link_slug IS NOT NULL AND paid) OR octet_length(payload)>64) THEN
  RAISE EXCEPTION 'cannot downgrade commerce while paid invoice links exist';
 END IF;
END; $$;
DROP TABLE bot_paid_media_purchases;
DROP TABLE bot_paid_media_files;
DROP TABLE bot_paid_media;
DROP TABLE bot_commerce_subscriptions;
DROP TABLE bot_commerce_checkouts;
DELETE FROM bot_invoices WHERE link_slug IS NOT NULL;
ALTER TABLE bot_invoices DROP CONSTRAINT bot_invoices_shape_check;
ALTER TABLE bot_invoices DROP COLUMN link_slug;
ALTER TABLE bot_invoices ALTER COLUMN payload TYPE varchar(64);
ALTER TABLE bot_invoices ADD CONSTRAINT bot_invoices_shape_check CHECK (
 bot_user_id>0 AND chat_id<>0 AND message_id>0 AND amount>0 AND date>0 AND currency='XTR' AND
 (NOT paid OR (charge_id<>'' AND payer_user_id>0 AND paid_at>0)) AND (NOT refunded OR paid)
);
DROP INDEX bot_invoices_message_idx;
CREATE UNIQUE INDEX bot_invoices_message_idx ON bot_invoices(bot_user_id,chat_id,message_id);
DROP TABLE bot_commerce_offers;
