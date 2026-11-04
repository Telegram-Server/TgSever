ALTER TABLE public.notify_settings
    DROP COLUMN IF EXISTS stories_sound,
    DROP COLUMN IF EXISTS sound;
