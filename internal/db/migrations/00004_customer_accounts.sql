-- +goose Up

-- Optional customer accounts (email + password). Guest checkout keeps working: an order's
-- account_id stays NULL unless it was placed while logged in, or the customer later added it
-- to their account by proving they know both its order number and checkout phone.
CREATE TABLE accounts (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT        NOT NULL,
    name          TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    -- Last delivery details, used to pre-fill checkout. Not verified; never used for access.
    phone         TEXT        NOT NULL DEFAULT '',
    district      TEXT        NOT NULL DEFAULT '',
    area          TEXT        NOT NULL DEFAULT '',
    address       TEXT        NOT NULL DEFAULT '',
    -- Bumped on password change; sessions holding an older value are logged out.
    session_epoch INT         NOT NULL DEFAULT 1,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX accounts_email_uq ON accounts (lower(email));

ALTER TABLE orders ADD COLUMN account_id BIGINT REFERENCES accounts (id) ON DELETE SET NULL;
CREATE INDEX orders_account_idx ON orders (account_id, created_at DESC) WHERE account_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS orders_account_idx;
ALTER TABLE orders DROP COLUMN IF EXISTS account_id;
DROP TABLE IF EXISTS accounts;
