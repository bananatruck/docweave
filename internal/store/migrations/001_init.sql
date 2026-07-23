CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS crawls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'completed', 'cancelled')),
    seed_urls jsonb NOT NULL,
    allowed_hosts text[] NOT NULL,
    max_depth integer NOT NULL CHECK (max_depth BETWEEN 0 AND 10),
    max_pages integer NOT NULL CHECK (max_pages BETWEEN 1 AND 100000),
    crawl_delay_ms integer NOT NULL CHECK (crawl_delay_ms BETWEEN 100 AND 60000),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz
);

CREATE TABLE IF NOT EXISTS frontier (
    id bigserial PRIMARY KEY,
    crawl_id uuid NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    url text NOT NULL,
    url_hash char(64) NOT NULL,
    depth integer NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'leased', 'completed', 'failed', 'skipped')),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (crawl_id, url_hash)
);
CREATE INDEX IF NOT EXISTS frontier_lease_idx ON frontier (status, available_at, lease_until);

CREATE TABLE IF NOT EXISTS pages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    crawl_id uuid NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    url text NOT NULL,
    url_hash char(64) NOT NULL,
    canonical_url text,
    title text NOT NULL DEFAULT '',
    text_content text NOT NULL DEFAULT '',
    content_hash char(64) NOT NULL,
    etag text,
    last_modified text,
    status_code integer NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('english'::regconfig, coalesce(title, '') || ' ' || coalesce(text_content, ''))
    ) STORED,
    UNIQUE (crawl_id, url_hash)
);
CREATE INDEX IF NOT EXISTS pages_search_idx ON pages USING gin(search_vector);

CREATE TABLE IF NOT EXISTS page_versions (
    id bigserial PRIMARY KEY,
    page_id uuid NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    content_hash char(64) NOT NULL,
    title text NOT NULL,
    text_content text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (page_id, content_hash)
);

CREATE TABLE IF NOT EXISTS links (
    crawl_id uuid NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    source_url_hash char(64) NOT NULL,
    target_url text NOT NULL,
    target_url_hash char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (crawl_id, source_url_hash, target_url_hash)
);

CREATE TABLE IF NOT EXISTS fetch_attempts (
    id bigserial PRIMARY KEY,
    crawl_id uuid NOT NULL REFERENCES crawls(id) ON DELETE CASCADE,
    frontier_id bigint NOT NULL REFERENCES frontier(id) ON DELETE CASCADE,
    worker_id text NOT NULL,
    status_code integer,
    duration_ms integer NOT NULL,
    error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS host_limits (
    host text PRIMARY KEY,
    next_allowed_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
