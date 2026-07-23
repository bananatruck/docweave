# Benchmark report

## 1,000-page single-host crawl

Measured on July 23, 2026 using commit working tree state before the initial commit.

| Item | Result |
|---|---:|
| Fixture pages requested | 1,000 |
| Pages persisted | 1,000 |
| Distinct normalized URL hashes | 1,000 |
| Fetch attempts | 1,000 |
| Fetch errors | 0 |
| Elapsed crawl time | 99.908 seconds |
| Effective throughput | 10.01 pages/second |
| p95 end-to-end work-item duration | 394 ms |
| Worker topology | 2 containers × 2 goroutines |
| Shared host delay | 100 ms |

Host: AMD Ryzen 9 8945HS, 8 cores / 16 threads, 14 GiB usable memory. Services ran through Docker Compose with PostgreSQL 17.

### Interpretation

This run is intentionally host-rate-limited to approximately 10 requests per second. The result demonstrates that four concurrent workers across two processes do not multiply the configured per-host request rate: they transact against one shared host budget. It is a correctness and politeness benchmark, not a claim about maximum network throughput.

The crawl completed with no fetch errors and no duplicate normalized URLs. The measured 10.01 pages/second is consistent with the configured 100 ms host budget.

### Reproduce

```bash
make benchmark
```

Create a crawl with:

```json
{
  "seed_urls": ["http://fixture:8081/page/0"],
  "allowed_hosts": ["fixture"],
  "max_depth": 3,
  "max_pages": 1000,
  "crawl_delay_ms": 100
}
```

Capture database results:

```sql
SELECT count(*), count(DISTINCT url_hash)
FROM pages
WHERE crawl_id = '<crawl-id>';

SELECT extract(epoch FROM (completed_at - started_at))
FROM crawls
WHERE id = '<crawl-id>';

SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms),
       count(*),
       count(*) FILTER (WHERE error IS NOT NULL)
FROM fetch_attempts
WHERE crawl_id = '<crawl-id>';
```

Future multi-host throughput results should be reported separately; they answer a different question from this host-politeness benchmark.
