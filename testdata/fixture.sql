-- Test fixture. Idempotent: safe to run before every test run.
DROP SCHEMA IF EXISTS public CASCADE;
DROP SCHEMA IF EXISTS other CASCADE;
CREATE SCHEMA public;
CREATE SCHEMA other;
GRANT USAGE ON SCHEMA public TO PUBLIC;

CREATE EXTENSION IF NOT EXISTS dblink SCHEMA public;

CREATE TABLE canary (id int PRIMARY KEY, note text NOT NULL);
INSERT INTO canary VALUES (1, 'alive');

CREATE SEQUENCE s;

CREATE TABLE users (
  id         bigint PRIMARY KEY,
  email      text NOT NULL UNIQUE,
  score      numeric(10,2),
  ratio      double precision,
  active     boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL,
  born       date,
  avatar     bytea,
  meta       jsonb,
  tags       text[],
  ext_id     uuid
);
CREATE INDEX users_created_at_idx ON users (created_at);
INSERT INTO users VALUES
  (1, 'a@example.com', 12.50, 0.5, true,  '2026-01-02 03:04:05+00', '1990-05-06', '\x0102', '{"k": 1}', '{x,y}', '00000000-0000-0000-0000-000000000001'),
  (2, 'b@example.com', NULL,  'NaN', false, '2026-02-03 04:05:06+00', NULL, NULL, NULL, NULL, NULL);

CREATE TABLE orders (
  id      int PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users (id),
  total   numeric NOT NULL
);

-- Same-named table in another schema: the old server mixed its columns into public.users.
CREATE TABLE other.users (id int, secret text);

CREATE PROCEDURE p() LANGUAGE sql AS $$ DELETE FROM canary $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'mcp_ro') THEN
    CREATE ROLE mcp_ro LOGIN PASSWORD 'mcp_ro';
  END IF;
END$$;
GRANT pg_read_all_data TO mcp_ro;

-- Not a superuser itself, but a member of one: must be refused too.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'mcp_member') THEN
    CREATE ROLE mcp_member LOGIN PASSWORD 'mcp_member';
  END IF;
END$$;
GRANT postgres TO mcp_member;
