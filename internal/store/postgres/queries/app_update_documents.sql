-- name: GetAppUpdateDocument :one
SELECT sha256, document_id, file_name, size, created_at
FROM app_update_documents
WHERE sha256 = $1;

-- PutAppUpdateDocument 记下一个安装包对应的文档；已有记录时保持原样（并发注册时先到者胜），
-- 调用方随后重读，以表里的那一行为准。
-- name: PutAppUpdateDocument :exec
INSERT INTO app_update_documents (sha256, document_id, file_name, size)
VALUES ($1, $2, $3, $4)
ON CONFLICT (sha256) DO NOTHING;
