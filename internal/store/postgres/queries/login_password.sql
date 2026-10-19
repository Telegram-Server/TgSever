-- name: GetLoginPassword :one
SELECT user_id, salt1, salt2, verifier, created_at, updated_at
FROM login_passwords
WHERE user_id = $1;

-- name: UpsertLoginPassword :exec
INSERT INTO login_passwords (user_id, salt1, salt2, verifier)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE
SET salt1 = EXCLUDED.salt1,
    salt2 = EXCLUDED.salt2,
    verifier = EXCLUDED.verifier,
    updated_at = now();

-- name: DeleteLoginPassword :execrows
DELETE FROM login_passwords WHERE user_id = $1;
