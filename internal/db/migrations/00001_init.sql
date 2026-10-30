-- +goose Up
-- All money columns are BIGINT poisha (1 BDT = 100 poisha) to avoid floating point errors.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------- Admin auth ----------
CREATE TABLE admins (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT        NOT NULL,
    name          TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('admin', 'staff')),
    active        BOOLEAN     NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admins_email_uq ON admins (lower(email));

-- Session store used by alexedwards/scs pgxstore.
CREATE TABLE sessions (
    token  TEXT PRIMARY KEY,
    data   BYTEA       NOT NULL,
    expiry TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

CREATE TABLE audit_logs (
    id         BIGSERIAL PRIMARY KEY,
    admin_id   BIGINT REFERENCES admins (id) ON DELETE SET NULL,
    action     TEXT        NOT NULL,
    entity     TEXT        NOT NULL DEFAULT '',
    entity_id  TEXT        NOT NULL DEFAULT '',
    details    JSONB       NOT NULL DEFAULT '{}',
    ip         TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);

-- ---------- Catalog ----------
CREATE TABLE categories (
    id          BIGSERIAL PRIMARY KEY,
    parent_id   BIGINT REFERENCES categories (id) ON DELETE SET NULL,
    name        TEXT        NOT NULL,
    slug        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    image_key   TEXT        NOT NULL DEFAULT '',
    sort_order  INT         NOT NULL DEFAULT 0,
    active      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX categories_slug_uq ON categories (slug) WHERE deleted_at IS NULL;
CREATE INDEX categories_parent_idx ON categories (parent_id);

CREATE TABLE products (
    id             BIGSERIAL PRIMARY KEY,
    category_id    BIGINT REFERENCES categories (id) ON DELETE SET NULL,
    name           TEXT        NOT NULL,
    slug           TEXT        NOT NULL,
    sku            TEXT        NOT NULL,
    brand          TEXT        NOT NULL DEFAULT '',
    description    TEXT        NOT NULL DEFAULT '',
    specs          JSONB       NOT NULL DEFAULT '[]',
    regular_price  BIGINT      NOT NULL CHECK (regular_price >= 0),
    selling_price  BIGINT      NOT NULL CHECK (selling_price >= 0),
    status         TEXT        NOT NULL DEFAULT 'draft'
                   CHECK (status IN ('draft', 'active', 'inactive', 'archived')),
    is_featured    BOOLEAN     NOT NULL DEFAULT FALSE,
    is_best_seller BOOLEAN     NOT NULL DEFAULT FALSE,
    sold_count     INT         NOT NULL DEFAULT 0,
    search         TSVECTOR GENERATED ALWAYS AS (
                       setweight(to_tsvector('simple', coalesce(name, '')), 'A') ||
                       setweight(to_tsvector('simple', coalesce(sku, '') || ' ' || coalesce(brand, '')), 'B') ||
                       setweight(to_tsvector('simple', coalesce(description, '')), 'C')
                   ) STORED,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    CONSTRAINT products_price_chk CHECK (selling_price <= regular_price)
);
CREATE UNIQUE INDEX products_slug_uq ON products (slug) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX products_sku_uq ON products (sku) WHERE deleted_at IS NULL;
CREATE INDEX products_category_idx ON products (category_id);
CREATE INDEX products_status_idx ON products (status) WHERE deleted_at IS NULL;
CREATE INDEX products_search_idx ON products USING GIN (search);
CREATE INDEX products_name_trgm_idx ON products USING GIN (name gin_trgm_ops);

CREATE TABLE product_images (
    id         BIGSERIAL PRIMARY KEY,
    product_id BIGINT      NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    object_key TEXT        NOT NULL,
    sort_order INT         NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX product_images_product_idx ON product_images (product_id, sort_order);

-- Every product has at least one variant; stock lives on the variant.
-- available = stock - reserved. Reserved units belong to placed-but-not-shipped orders.
CREATE TABLE product_variants (
    id                  BIGSERIAL PRIMARY KEY,
    product_id          BIGINT      NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    sku                 TEXT        NOT NULL,
    name                TEXT        NOT NULL DEFAULT '',
    stock               INT         NOT NULL DEFAULT 0 CHECK (stock >= 0),
    reserved            INT         NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    low_stock_threshold INT         NOT NULL DEFAULT 5,
    sort_order          INT         NOT NULL DEFAULT 0,
    active              BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT product_variants_reserved_chk CHECK (reserved <= stock)
);
CREATE UNIQUE INDEX product_variants_sku_uq ON product_variants (sku);
CREATE INDEX product_variants_product_idx ON product_variants (product_id);

-- ---------- Customers & delivery ----------
CREATE TABLE customers (
    id         BIGSERIAL PRIMARY KEY,
    phone      TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    district   TEXT        NOT NULL DEFAULT '',
    area       TEXT        NOT NULL DEFAULT '',
    address    TEXT        NOT NULL DEFAULT '',
    notes      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT customers_phone_uq UNIQUE (phone)
);

CREATE TABLE delivery_zones (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT    NOT NULL,
    charge     BIGINT  NOT NULL CHECK (charge >= 0),
    sort_order INT     NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT TRUE
);

-- ---------- Orders ----------
CREATE TABLE orders (
    id               BIGSERIAL PRIMARY KEY,
    order_number     TEXT        NOT NULL,
    idempotency_key  TEXT        NOT NULL,
    customer_id      BIGINT      NOT NULL REFERENCES customers (id),
    customer_name    TEXT        NOT NULL,
    phone            TEXT        NOT NULL,
    delivery_zone_id BIGINT REFERENCES delivery_zones (id) ON DELETE SET NULL,
    zone_name        TEXT        NOT NULL,
    district         TEXT        NOT NULL,
    area             TEXT        NOT NULL,
    address          TEXT        NOT NULL,
    delivery_notes   TEXT        NOT NULL DEFAULT '',
    payment_method   TEXT        NOT NULL DEFAULT 'cod' CHECK (payment_method IN ('cod')),
    payment_status   TEXT        NOT NULL DEFAULT 'unpaid' CHECK (payment_status IN ('unpaid', 'paid', 'refunded')),
    status           TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN (
                         'pending', 'confirmed', 'processing', 'ready_to_ship',
                         'shipped', 'in_transit', 'delivered', 'cancelled', 'returned')),
    subtotal         BIGINT      NOT NULL,
    discount_total   BIGINT      NOT NULL,
    delivery_charge  BIGINT      NOT NULL,
    total            BIGINT      NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT orders_order_number_uq UNIQUE (order_number),
    CONSTRAINT orders_idempotency_key_uq UNIQUE (idempotency_key)
);
CREATE INDEX orders_phone_idx ON orders (phone);
CREATE INDEX orders_status_idx ON orders (status);
CREATE INDEX orders_created_idx ON orders (created_at DESC);
CREATE INDEX orders_customer_idx ON orders (customer_id);

-- Item rows snapshot product data so historical orders stay accurate after catalog edits.
CREATE TABLE order_items (
    id            BIGSERIAL PRIMARY KEY,
    order_id      BIGINT NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id    BIGINT REFERENCES products (id) ON DELETE SET NULL,
    variant_id    BIGINT REFERENCES product_variants (id) ON DELETE SET NULL,
    product_name  TEXT   NOT NULL,
    variant_name  TEXT   NOT NULL DEFAULT '',
    sku           TEXT   NOT NULL,
    image_key     TEXT   NOT NULL DEFAULT '',
    regular_price BIGINT NOT NULL,
    unit_price    BIGINT NOT NULL,
    quantity      INT    NOT NULL CHECK (quantity > 0),
    line_total    BIGINT NOT NULL
);
CREATE INDEX order_items_order_idx ON order_items (order_id);
CREATE INDEX order_items_product_idx ON order_items (product_id);

CREATE TABLE order_status_history (
    id          BIGSERIAL PRIMARY KEY,
    order_id    BIGINT      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    from_status TEXT,
    to_status   TEXT        NOT NULL,
    note        TEXT        NOT NULL DEFAULT '',
    admin_id    BIGINT REFERENCES admins (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX order_status_history_order_idx ON order_status_history (order_id, created_at);

CREATE TABLE order_notes (
    id         BIGSERIAL PRIMARY KEY,
    order_id   BIGINT      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    admin_id   BIGINT REFERENCES admins (id) ON DELETE SET NULL,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX order_notes_order_idx ON order_notes (order_id);

-- ---------- Inventory ledger ----------
CREATE TABLE inventory_movements (
    id              BIGSERIAL PRIMARY KEY,
    variant_id      BIGINT      NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('initial', 'adjustment', 'reserve', 'release', 'ship', 'return')),
    stock_change    INT         NOT NULL DEFAULT 0,
    reserved_change INT         NOT NULL DEFAULT 0,
    stock_after     INT         NOT NULL,
    reserved_after  INT         NOT NULL,
    order_id        BIGINT REFERENCES orders (id) ON DELETE SET NULL,
    admin_id        BIGINT REFERENCES admins (id) ON DELETE SET NULL,
    note            TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inventory_movements_variant_idx ON inventory_movements (variant_id, created_at DESC);

-- ---------- Settings ----------
CREATE TABLE store_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO store_settings (key, value) VALUES
    ('store_name', 'Bazaar BD'),
    ('store_tagline', 'Quality products, delivered across Bangladesh'),
    ('store_phone', '01700000000'),
    ('store_email', 'support@example.com'),
    ('store_address', 'Dhaka, Bangladesh'),
    ('facebook_url', ''),
    ('instagram_url', ''),
    ('hero_title', 'Shop smarter. Pay on delivery.'),
    ('hero_subtitle', 'Thousands of products with Cash on Delivery all over Bangladesh.'),
    ('free_delivery_threshold', '0'),
    ('cod_enabled', 'true'),
    ('meta_description', 'Online shopping in Bangladesh with Cash on Delivery.'),
    ('policy_text', 'Products can be returned within 7 days of delivery if unused and in original packaging.');

INSERT INTO delivery_zones (name, charge, sort_order) VALUES
    ('Inside Dhaka', 7000, 1),
    ('Dhaka Suburbs', 10000, 2),
    ('Outside Dhaka', 13000, 3);

-- +goose Down
DROP TABLE IF EXISTS store_settings, inventory_movements, order_notes, order_status_history,
    order_items, orders, delivery_zones, customers, product_variants, product_images,
    products, categories, audit_logs, sessions, admins CASCADE;
