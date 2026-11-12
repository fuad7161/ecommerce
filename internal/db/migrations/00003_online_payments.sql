-- +goose Up

-- Orders can now be paid online (SSLCommerz) as well as cash on delivery.
ALTER TABLE orders DROP CONSTRAINT orders_payment_method_check;
ALTER TABLE orders ADD CONSTRAINT orders_payment_method_check CHECK (payment_method IN ('cod', 'online'));
ALTER TABLE orders ADD COLUMN paid_at TIMESTAMPTZ;
CREATE INDEX orders_unpaid_online_idx ON orders (created_at)
    WHERE payment_method = 'online' AND payment_status = 'unpaid' AND status = 'pending';

-- One row per payment attempt. An order can have several attempts (customer retries);
-- at most one of them ends up 'paid'.
CREATE TABLE payments (
    id           BIGSERIAL PRIMARY KEY,
    order_id     BIGINT      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    provider     TEXT        NOT NULL DEFAULT 'sslcommerz',
    tran_id      TEXT        NOT NULL,
    amount       BIGINT      NOT NULL CHECK (amount > 0), -- poisha
    status       TEXT        NOT NULL DEFAULT 'initiated'
                     CHECK (status IN ('initiated', 'paid', 'failed', 'cancelled')),
    val_id       TEXT        NOT NULL DEFAULT '',
    bank_tran_id TEXT        NOT NULL DEFAULT '',
    card_type    TEXT        NOT NULL DEFAULT '',
    risk_level   TEXT        NOT NULL DEFAULT '',
    risk_title   TEXT        NOT NULL DEFAULT '',
    last_error   TEXT        NOT NULL DEFAULT '',
    gateway_response JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT payments_tran_id_uq UNIQUE (tran_id)
);
CREATE INDEX payments_order_idx ON payments (order_id);
CREATE UNIQUE INDEX payments_one_paid_uq ON payments (order_id) WHERE status = 'paid';

INSERT INTO store_settings (key, value) VALUES ('online_payment_enabled', 'true') ON CONFLICT (key) DO NOTHING;
UPDATE store_settings SET value = 'Shop smarter. Pay your way.' WHERE key = 'hero_title' AND value = 'Shop smarter. Pay on delivery.';
UPDATE store_settings SET value = 'Thousands of products delivered all over Bangladesh. Pay online with bKash, Nagad or card, or cash on delivery.'
    WHERE key = 'hero_subtitle' AND value = 'Thousands of products with Cash on Delivery all over Bangladesh.';

-- +goose Down
DELETE FROM store_settings WHERE key = 'online_payment_enabled';
DROP TABLE IF EXISTS payments;
DROP INDEX IF EXISTS orders_unpaid_online_idx;
ALTER TABLE orders DROP COLUMN IF EXISTS paid_at;
UPDATE orders SET payment_method = 'cod' WHERE payment_method = 'online';
ALTER TABLE orders DROP CONSTRAINT orders_payment_method_check;
ALTER TABLE orders ADD CONSTRAINT orders_payment_method_check CHECK (payment_method IN ('cod'));
