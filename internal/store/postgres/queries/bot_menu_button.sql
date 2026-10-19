-- name: UpsertBotUserMenuButton :exec
INSERT INTO bot_user_menu_buttons (bot_user_id, user_id, menu_button_type, menu_button_text, menu_button_url)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (bot_user_id, user_id) DO UPDATE
SET menu_button_type = EXCLUDED.menu_button_type,
    menu_button_text = EXCLUDED.menu_button_text,
    menu_button_url = EXCLUDED.menu_button_url,
    updated_at = now();

-- name: DeleteBotUserMenuButton :exec
DELETE FROM bot_user_menu_buttons WHERE bot_user_id = $1 AND user_id = $2;

-- name: GetBotUserMenuButton :one
SELECT menu_button_type, menu_button_text, menu_button_url
FROM bot_user_menu_buttons
WHERE bot_user_id = $1 AND user_id = $2;
