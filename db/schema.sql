-- Schema for the distributed-systems playground.
--
-- This file is mounted into the official postgres image at
-- /docker-entrypoint-initdb.d/schema.sql. The image runs every .sql file
-- found there ONCE, the first time the data directory is empty.
--
-- That "only on first init" behaviour is itself a learning moment: if you
-- change this file later, you must wipe the volume for it to re-run
-- (see the README, "Resetting the database").

-- Orders are written by order-service.
CREATE TABLE IF NOT EXISTS orders (
    id          SERIAL PRIMARY KEY,
    item        TEXT            NOT NULL,
    amount_cents INTEGER        NOT NULL,
    -- pending  -> the order row exists but payment has not been attempted/finished
    -- paid     -> payment-service approved it
    -- failed   -> payment-service declined it or was unreachable
    status      TEXT            NOT NULL DEFAULT 'pending',
    created_at  TIMESTAMPTZ     NOT NULL DEFAULT now()
);

-- Payments are written by payment-service. Notice both services share ONE
-- database here. That is deliberately a little wrong: a "shared database"
-- between microservices is a classic coupling anti-pattern you will later
-- want to untangle. For now it keeps the playground small.
CREATE TABLE IF NOT EXISTS payments (
    id          SERIAL PRIMARY KEY,
    order_id    INTEGER         NOT NULL,
    amount_cents INTEGER        NOT NULL,
    -- approved or declined
    outcome     TEXT            NOT NULL,
    created_at  TIMESTAMPTZ     NOT NULL DEFAULT now()
);
