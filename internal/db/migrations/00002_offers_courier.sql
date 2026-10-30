-- +goose Up

-- ---------- Offers / campaigns ----------
-- A campaign discounts the REGULAR price of the products it targets (directly or via
-- category, including subcategories). Discounts never stack: the customer pays the lowest
-- of the product's own selling price and the best active campaign price.
CREATE TABLE offers (
    id             BIGSERIAL PRIMARY KEY,
    title          TEXT        NOT NULL,
    slug           TEXT        NOT NULL,
    description    TEXT        NOT NULL DEFAULT '',
    banner_key     TEXT        NOT NULL DEFAULT '',
    discount_type  TEXT        NOT NULL CHECK (discount_type IN ('percent', 'fixed')),
    -- percent: 1..90 (whole percent); fixed: poisha
    discount_value BIGINT      NOT NULL CHECK (discount_value > 0),
    starts_at      TIMESTAMPTZ NOT NULL,
    ends_at        TIMESTAMPTZ NOT NULL,
    active         BOOLEAN     NOT NULL DEFAULT TRUE,
    show_on_home   BOOLEAN     NOT NULL DEFAULT TRUE,
    sort_order     INT         NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    CONSTRAINT offers_dates_chk CHECK (ends_at > starts_at),
    CONSTRAINT offers_percent_chk CHECK (discount_type <> 'percent' OR discount_value BETWEEN 1 AND 90)
);
CREATE UNIQUE INDEX offers_slug_uq ON offers (slug) WHERE deleted_at IS NULL;
CREATE INDEX offers_active_idx ON offers (starts_at, ends_at) WHERE active AND deleted_at IS NULL;

CREATE TABLE offer_products (
    offer_id   BIGINT NOT NULL REFERENCES offers (id) ON DELETE CASCADE,
    product_id BIGINT NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    PRIMARY KEY (offer_id, product_id)
);
CREATE INDEX offer_products_product_idx ON offer_products (product_id);

CREATE TABLE offer_categories (
    offer_id    BIGINT NOT NULL REFERENCES offers (id) ON DELETE CASCADE,
    category_id BIGINT NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    PRIMARY KEY (offer_id, category_id)
);
CREATE INDEX offer_categories_category_idx ON offer_categories (category_id);

-- best_offer returns the cheapest currently-running campaign price for a product.
-- +goose StatementBegin
CREATE FUNCTION best_offer(p_product_id BIGINT, p_category_id BIGINT, p_regular BIGINT,
                           OUT offer_id BIGINT, OUT offer_title TEXT, OUT offer_slug TEXT,
                           OUT offer_ends_at TIMESTAMPTZ, OUT offer_price BIGINT)
LANGUAGE sql STABLE AS $$
    SELECT o.id, o.title, o.slug, o.ends_at,
           CASE WHEN o.discount_type = 'percent'
                THEN p_regular - (p_regular * o.discount_value / 100)
                ELSE greatest(p_regular - o.discount_value, 0) END AS price
    FROM offers o
    WHERE o.active AND o.deleted_at IS NULL AND now() >= o.starts_at AND now() < o.ends_at
      AND (EXISTS (SELECT 1 FROM offer_products op WHERE op.offer_id = o.id AND op.product_id = p_product_id)
           OR EXISTS (SELECT 1 FROM offer_categories oc JOIN categories c ON c.id = p_category_id
                      WHERE oc.offer_id = o.id AND (oc.category_id = c.id OR oc.category_id = c.parent_id)))
    ORDER BY price ASC, o.id
    LIMIT 1
$$;
-- +goose StatementEnd

ALTER TABLE order_items ADD COLUMN offer_title TEXT NOT NULL DEFAULT '';

-- ---------- Homepage sections ----------
-- Ordered, comma-separated list of enabled section keys.
INSERT INTO store_settings (key, value) VALUES
    ('home_sections', 'campaigns,categories,offers,featured,trending,best_sellers,new_arrivals')
ON CONFLICT (key) DO NOTHING;

-- ---------- Courier (Steadfast) ----------
CREATE TABLE courier_consignments (
    id              BIGSERIAL PRIMARY KEY,
    order_id        BIGINT      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    provider        TEXT        NOT NULL DEFAULT 'steadfast',
    -- creating: request in flight; created: consignment exists; failed: last attempt failed (retry allowed)
    state           TEXT        NOT NULL CHECK (state IN ('creating', 'created', 'failed')),
    consignment_id  BIGINT,
    tracking_code   TEXT        NOT NULL DEFAULT '',
    invoice         TEXT        NOT NULL,
    cod_amount      BIGINT      NOT NULL,
    delivery_status TEXT        NOT NULL DEFAULT '',
    last_error      TEXT        NOT NULL DEFAULT '',
    created_by      BIGINT REFERENCES admins (id) ON DELETE SET NULL,
    last_synced_at  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT courier_consignments_order_uq UNIQUE (order_id)
);
CREATE UNIQUE INDEX courier_consignments_cid_uq ON courier_consignments (provider, consignment_id) WHERE consignment_id IS NOT NULL;
CREATE INDEX courier_consignments_sync_idx ON courier_consignments (last_synced_at) WHERE state = 'created';

-- Every request/response exchanged with the courier, for support and auditing.
CREATE TABLE courier_events (
    id          BIGSERIAL PRIMARY KEY,
    order_id    BIGINT      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    kind        TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT '',
    http_status INT         NOT NULL DEFAULT 0,
    request     JSONB,
    response    TEXT        NOT NULL DEFAULT '',
    error       TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX courier_events_order_idx ON courier_events (order_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS courier_events, courier_consignments;
DELETE FROM store_settings WHERE key = 'home_sections';
ALTER TABLE order_items DROP COLUMN IF EXISTS offer_title;
DROP FUNCTION IF EXISTS best_offer(BIGINT, BIGINT, BIGINT);
DROP TABLE IF EXISTS offer_categories, offer_products, offers;
