CREATE INDEX CONCURRENTLY IF NOT EXISTS message_boxes_audit_seek_idx
    ON public.message_boxes (owner_user_id, peer_type, peer_id, box_id DESC);
