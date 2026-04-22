CREATE TABLE IF NOT EXISTS products (
    id       SERIAL PRIMARY KEY,
    name     TEXT           NOT NULL,
    category TEXT           NOT NULL,
    price    NUMERIC(10, 2) NOT NULL
);

CREATE TABLE IF NOT EXISTS orders (
    id          BIGSERIAL PRIMARY KEY,
    customer_id INT            NOT NULL,
    product_id  INT            NOT NULL REFERENCES products (id),
    amount      NUMERIC(10, 2) NOT NULL,
    created_at  TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

INSERT INTO products (name, category, price)
VALUES ('Laptop Pro', 'Electronics', 1299.99),
       ('Wireless Mouse', 'Electronics', 29.99),
       ('Standing Desk', 'Furniture', 499.99),
       ('Office Chair', 'Furniture', 299.99),
       ('Coffee Maker', 'Appliances', 79.99),
       ('Notebook 5-pack', 'Stationery', 12.99),
       ('Mechanical Keyboard', 'Electronics', 149.99),
       ('Monitor 27"', 'Electronics', 349.99),
       ('Desk Lamp', 'Furniture', 49.99),
       ('USB-C Hub', 'Electronics', 59.99);

-- Seed 500 000 orders so aggregations are expensive (~100–500 ms without snapshot)
INSERT INTO orders (customer_id, product_id, amount)
SELECT (random() * 9999 + 1)::INT,
       (floor(random() * 10) + 1)::INT,
       round((random() * 990 + 10)::numeric, 2)
FROM generate_series(1, 500000);

CREATE INDEX IF NOT EXISTS idx_orders_product_id ON orders (product_id);
CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders (created_at);
