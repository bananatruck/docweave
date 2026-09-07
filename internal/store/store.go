package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

type Crawl struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	SeedURLs     []string   `json:"seed_urls"`
	AllowedHosts []string   `json:"allowed_hosts"`
	MaxDepth     int        `json:"max_depth"`
	MaxPages     int        `json:"max_pages"`
	CrawlDelayMS int        `json:"crawl_delay_ms"`
	Queued       int        `json:"queued"`
	Leased       int        `json:"leased"`
	Completed    int        `json:"completed"`
	Failed       int        `json:"failed"`
	Pages        int        `json:"pages"`
	Changes      int        `json:"changes"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

type Work struct {
	ID           int64
	CrawlID      string
	URL          string
	URLHash      string
	Depth        int
	Attempts     int
	AllowedHosts []string
	MaxDepth     int
	MaxPages     int
	CrawlDelay   time.Duration
}

type PageInput struct {
	URL, URLHash, CanonicalURL, Title, Text, ContentHash, ETag, LastModified string
	StatusCode                                                               int
}

type Page struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	CanonicalURL string    `json:"canonical_url,omitempty"`
	Title        string    `json:"title"`
	TextPreview  string    `json:"text_preview"`
	ContentHash  string    `json:"content_hash"`
	StatusCode   int       `json:"status_code"`
	FetchedAt    time.Time `json:"fetched_at"`
	Versions     int       `json:"versions"`
}

type Change struct {
	PageID       string    `json:"page_id"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	VersionCount int       `json:"version_count"`
	LastChanged  time.Time `json:"last_changed"`
}

type Version struct {
	ID          int64     `json:"id"`
	ContentHash string    `json:"content_hash"`
	Title       string    `json:"title"`
	Text        string    `json:"text"`
	CreatedAt   time.Time `json:"created_at"`
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Store) Ping(ctx context.Context) error {
	return s.Pool.Ping(ctx)
}

func (s *Store) CreateCrawl(ctx context.Context, seeds, hosts []string, maxDepth, maxPages, delayMS int) (string, error) {
	seedsJSON, _ := json.Marshal(seeds)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO crawls(seed_urls, allowed_hosts, max_depth, max_pages, crawl_delay_ms)
		VALUES($1,$2,$3,$4,$5) RETURNING id`, seedsJSON, hosts, maxDepth, maxPages, delayMS).Scan(&id)
	if err != nil {
		return "", err
	}
	for _, seed := range seeds {
		if _, err := tx.Exec(ctx, `INSERT INTO frontier(crawl_id,url,url_hash,depth) VALUES($1,$2,$3,0)
			ON CONFLICT DO NOTHING`, id, seed, Hash(seed)); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

func (s *Store) Lease(ctx context.Context, workerID string, ttl time.Duration) (*Work, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var w Work
	var delayMS int
	err = tx.QueryRow(ctx, `WITH candidate AS (
			SELECT f.id FROM frontier f JOIN crawls c ON c.id=f.crawl_id
			WHERE c.status IN ('queued','running')
			  AND ((f.status='queued' AND f.available_at<=now()) OR (f.status='leased' AND f.lease_until<now()))
			ORDER BY f.available_at, f.id FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE frontier f SET status='leased', lease_owner=$1, lease_until=now()+$2::interval,
			attempts=attempts+1, updated_at=now()
		FROM candidate, crawls c WHERE f.id=candidate.id AND c.id=f.crawl_id
		RETURNING f.id,f.crawl_id,f.url,f.url_hash,f.depth,f.attempts,c.allowed_hosts,
			c.max_depth,c.max_pages,c.crawl_delay_ms`,
		workerID, ttl.String()).Scan(&w.ID, &w.CrawlID, &w.URL, &w.URLHash, &w.Depth, &w.Attempts,
		&w.AllowedHosts, &w.MaxDepth, &w.MaxPages, &delayMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.CrawlDelay = time.Duration(delayMS) * time.Millisecond
	if _, err := tx.Exec(ctx, `UPDATE crawls SET status='running', started_at=coalesce(started_at,now())
		WHERE id=$1 AND status='queued'`, w.CrawlID); err != nil {
		return nil, err
	}
	return &w, tx.Commit(ctx)
}

func (s *Store) ReserveHost(ctx context.Context, host string, delay time.Duration) (time.Duration, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO host_limits(host) VALUES($1) ON CONFLICT DO NOTHING`, host); err != nil {
		return 0, err
	}
	var next time.Time
	if err := tx.QueryRow(ctx, `SELECT next_allowed_at FROM host_limits WHERE host=$1 FOR UPDATE`, host).Scan(&next); err != nil {
		return 0, err
	}
	now := time.Now()
	start := now
	if next.After(start) {
		start = next
	}
	if _, err := tx.Exec(ctx, `UPDATE host_limits SET next_allowed_at=$2,updated_at=now() WHERE host=$1`,
		host, start.Add(delay)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if next.After(now) {
		return time.Until(next), nil
	}
	return 0, nil
}

func (s *Store) Complete(ctx context.Context, w *Work, page PageInput, links []string, duration time.Duration, workerID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM crawls WHERE id=$1 FOR UPDATE`, w.CrawlID); err != nil {
		return err
	}
	var pageID string
	err = tx.QueryRow(ctx, `INSERT INTO pages(crawl_id,url,url_hash,canonical_url,title,text_content,content_hash,etag,last_modified,status_code)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(crawl_id,url_hash) DO UPDATE SET canonical_url=excluded.canonical_url,title=excluded.title,
			text_content=excluded.text_content,content_hash=excluded.content_hash,etag=excluded.etag,
			last_modified=excluded.last_modified,status_code=excluded.status_code,fetched_at=now()
		RETURNING id`, w.CrawlID, page.URL, page.URLHash, page.CanonicalURL, page.Title, page.Text,
		page.ContentHash, page.ETag, page.LastModified, page.StatusCode).Scan(&pageID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO page_versions(page_id,content_hash,title,text_content)
		VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, pageID, page.ContentHash, page.Title, page.Text); err != nil {
		return err
	}
	for _, target := range links {
		hash := Hash(target)
		if _, err := tx.Exec(ctx, `INSERT INTO links(crawl_id,source_url_hash,target_url,target_url_hash)
			VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, w.CrawlID, w.URLHash, target, hash); err != nil {
			return err
		}
		if w.Depth < w.MaxDepth {
			if _, err := tx.Exec(ctx, `INSERT INTO frontier(crawl_id,url,url_hash,depth)
				SELECT $1,$2,$3,$4 WHERE (SELECT count(*) FROM frontier WHERE crawl_id=$1) < $5
				ON CONFLICT DO NOTHING`, w.CrawlID, target, hash, w.Depth+1, w.MaxPages); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE frontier SET status='completed',lease_owner=NULL,lease_until=NULL,updated_at=now()
		WHERE id=$1`, w.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fetch_attempts(crawl_id,frontier_id,worker_id,status_code,duration_ms)
		VALUES($1,$2,$3,$4,$5)`, w.CrawlID, w.ID, workerID, page.StatusCode, duration.Milliseconds()); err != nil {
		return err
	}
	if err := markComplete(ctx, tx, w.CrawlID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) NotModified(ctx context.Context, w *Work, status int, duration time.Duration, workerID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE pages SET fetched_at=now(),status_code=$2 WHERE crawl_id=$1 AND url_hash=$3`,
		w.CrawlID, status, w.URLHash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE frontier SET status='completed',lease_owner=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, w.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fetch_attempts(crawl_id,frontier_id,worker_id,status_code,duration_ms)
		VALUES($1,$2,$3,$4,$5)`, w.CrawlID, w.ID, workerID, status, duration.Milliseconds()); err != nil {
		return err
	}
	if err := markComplete(ctx, tx, w.CrawlID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func markComplete(ctx context.Context, tx pgx.Tx, crawlID string) error {
	_, err := tx.Exec(ctx, `UPDATE crawls SET status='completed',completed_at=now()
		WHERE id=$1 AND NOT EXISTS (
			SELECT 1 FROM frontier WHERE crawl_id=$1 AND status IN ('queued','leased')
		)`, crawlID)
	return err
}

func (s *Store) Fail(ctx context.Context, w *Work, cause error, duration time.Duration, workerID string, statusCode int, retryable bool) error {
	retry := retryable && w.Attempts < 3
	status := "failed"
	available := time.Now()
	if retry {
		status = "queued"
		available = available.Add(time.Duration(1<<uint(w.Attempts-1)) * time.Second)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	message := cause.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err = tx.Exec(ctx, `UPDATE frontier SET status=$2,available_at=$3,lease_owner=NULL,lease_until=NULL,
		last_error=$4,updated_at=now() WHERE id=$1`, w.ID, status, available, message)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO fetch_attempts(crawl_id,frontier_id,worker_id,status_code,duration_ms,error)
			VALUES($1,$2,$3,$4,$5,$6)`, w.CrawlID, w.ID, workerID, nullableStatus(statusCode), duration.Milliseconds(), message)
	}
	if err == nil && !retry {
		err = markComplete(ctx, tx, w.CrawlID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Skip(ctx context.Context, w *Work, reason string, duration time.Duration, workerID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE frontier SET status='skipped',lease_owner=NULL,lease_until=NULL,
		last_error=$2,updated_at=now() WHERE id=$1`, w.ID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fetch_attempts(crawl_id,frontier_id,worker_id,duration_ms,error)
		VALUES($1,$2,$3,$4,$5)`, w.CrawlID, w.ID, workerID, duration.Milliseconds(), reason); err != nil {
		return err
	}
	if err := markComplete(ctx, tx, w.CrawlID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableStatus(status int) any {
	if status == 0 {
		return nil
	}
	return status
}

func (s *Store) PreviousHeaders(ctx context.Context, crawlID, hash string) (string, string) {
	var etag, modified *string
	err := s.Pool.QueryRow(ctx, `SELECT etag,last_modified FROM pages WHERE crawl_id=$1 AND url_hash=$2`,
		crawlID, hash).Scan(&etag, &modified)
	if err != nil {
		return "", ""
	}
	if etag != nil {
		return *etag, valueOrEmpty(modified)
	}
	return "", valueOrEmpty(modified)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Store) GetCrawl(ctx context.Context, id string) (Crawl, error) {
	var c Crawl
	var seedJSON []byte
	err := s.Pool.QueryRow(ctx, `SELECT c.id,c.status,c.seed_urls,c.allowed_hosts,c.max_depth,c.max_pages,
		c.crawl_delay_ms,c.created_at,c.started_at,c.completed_at,
		count(*) FILTER(WHERE f.status='queued'),count(*) FILTER(WHERE f.status='leased'),
		count(*) FILTER(WHERE f.status='completed'),count(*) FILTER(WHERE f.status='failed'),
		(SELECT count(*) FROM pages p WHERE p.crawl_id=c.id),
		(SELECT count(*) FROM pages p JOIN page_versions v ON v.page_id=p.id WHERE p.crawl_id=c.id) -
			(SELECT count(*) FROM pages p WHERE p.crawl_id=c.id)
		FROM crawls c LEFT JOIN frontier f ON f.crawl_id=c.id WHERE c.id=$1 GROUP BY c.id`, id).
		Scan(&c.ID, &c.Status, &seedJSON, &c.AllowedHosts, &c.MaxDepth, &c.MaxPages, &c.CrawlDelayMS,
			&c.CreatedAt, &c.StartedAt, &c.CompletedAt, &c.Queued, &c.Leased, &c.Completed, &c.Failed,
			&c.Pages, &c.Changes)
	if err != nil {
		return Crawl{}, err
	}
	_ = json.Unmarshal(seedJSON, &c.SeedURLs)
	return c, nil
}

func (s *Store) ListCrawls(ctx context.Context, limit int) ([]Crawl, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM crawls ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	result := make([]Crawl, 0, len(ids))
	for _, id := range ids {
		c, err := s.GetCrawl(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}

func (s *Store) ListPages(ctx context.Context, crawlID, query string, limit int) ([]Page, error) {
	where := "p.crawl_id=$1"
	args := []any{crawlID, limit}
	if query != "" {
		where += " AND p.search_vector @@ websearch_to_tsquery('english'::regconfig,$3)"
		args = append(args, query)
	}
	sql := fmt.Sprintf(`SELECT p.id,p.url,coalesce(p.canonical_url,''),p.title,left(p.text_content,280),
		p.content_hash,p.status_code,p.fetched_at,count(v.id)
		FROM pages p LEFT JOIN page_versions v ON v.page_id=p.id WHERE %s
		GROUP BY p.id ORDER BY p.fetched_at DESC LIMIT $2`, where)
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Page
	for rows.Next() {
		var p Page
		if err := rows.Scan(&p.ID, &p.URL, &p.CanonicalURL, &p.Title, &p.TextPreview,
			&p.ContentHash, &p.StatusCode, &p.FetchedAt, &p.Versions); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) ListChanges(ctx context.Context, crawlID string) ([]Change, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id,p.url,p.title,count(v.id),max(v.created_at)
		FROM pages p JOIN page_versions v ON v.page_id=p.id WHERE p.crawl_id=$1
		GROUP BY p.id HAVING count(v.id)>1 ORDER BY max(v.created_at) DESC LIMIT 100`, crawlID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Change
	for rows.Next() {
		var item Change
		if err := rows.Scan(&item.PageID, &item.URL, &item.Title, &item.VersionCount, &item.LastChanged); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Recrawl(ctx context.Context, crawlID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE crawls SET status='queued',started_at=NULL,completed_at=NULL WHERE id=$1`, crawlID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	_, err = s.Pool.Exec(ctx, `UPDATE frontier SET status='queued',available_at=now(),lease_owner=NULL,
		lease_until=NULL,attempts=0,last_error=NULL,updated_at=now() WHERE crawl_id=$1`, crawlID)
	return err
}

func (s *Store) GetVersions(ctx context.Context, crawlID, pageID string) ([]Version, error) {
	rows, err := s.Pool.Query(ctx, `SELECT v.id,v.content_hash,v.title,v.text_content,v.created_at
		FROM page_versions v JOIN pages p ON p.id=v.page_id
		WHERE p.crawl_id=$1 AND p.id=$2 ORDER BY v.created_at DESC`, crawlID, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Version
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.ID, &version.ContentHash, &version.Title, &version.Text, &version.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, version)
	}
	return result, rows.Err()
}
