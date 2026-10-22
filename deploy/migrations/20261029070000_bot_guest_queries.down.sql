ALTER TABLE bots DROP COLUMN inline_feedback_percentage;
ALTER TABLE webview_requested_buttons
 DROP COLUMN suggested_username,
 DROP COLUMN suggested_name,
 DROP COLUMN icon_custom_emoji_id,
 DROP COLUMN style;
DROP TABLE bot_guest_queries;
