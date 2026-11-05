-- 客户端更新安装包注册成的 Telegram 文档（docs/update-service.md 的 Android 一节）。
-- 同一个安装包（按 SHA-256）只入库一次：telesrv 重启、清单重载、多个通道引用同一个包都
-- 复用这一行指向的文档，help.appUpdate 随 document 下发它。
CREATE TABLE app_update_documents (
    sha256 TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    document_id BIGINT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    file_name TEXT NOT NULL,
    size BIGINT NOT NULL CHECK (size > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
