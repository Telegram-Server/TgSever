-- 账号级已保存通知铃声（account.getSavedRingtones / saveRingtone / uploadRingtone）。
-- seq 只用来排序：最新保存的在前。
CREATE TABLE public.saved_ringtones (
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    document_id bigint NOT NULL REFERENCES public.documents(id) ON DELETE CASCADE,
    seq bigint GENERATED ALWAYS AS IDENTITY,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (user_id, document_id)
);

CREATE INDEX saved_ringtones_user_seq_idx ON public.saved_ringtones USING btree (user_id, seq DESC);
