-- bot 被加为群 / 频道管理员时建议的默认权限（bots.setBotGroupDefaultAdminRights /
-- setBotBroadcastDefaultAdminRights）。jsonb 与 channel_members.admin_rights 同形
--（domain.ChannelAdminRights 的字段名），'{}' 表示未设置。
ALTER TABLE bots
    ADD COLUMN group_admin_rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    ADD COLUMN broadcast_admin_rights jsonb DEFAULT '{}'::jsonb NOT NULL;
