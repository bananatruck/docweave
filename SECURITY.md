# Security policy

DocWeave intentionally supports only HTTP and HTTPS crawling. It validates DNS results before every connection, blocks private and special-use IP ranges, revalidates redirects, limits ports, response sizes, redirects, and timeouts, and restricts every crawl to explicit hosts.

`DOCWEAVE_ALLOW_PRIVATE_NETWORKS=true` exists only for the local synthetic benchmark. Configuration validation prevents combining it with public-demo mode.

The hosted write API requires an API key and is further restricted by `DOCWEAVE_DEMO_HOSTS`. Do not use DocWeave to bypass authentication, access controls, CAPTCHAs, paywalls, or a site's published crawling policy.

Report vulnerabilities privately through GitHub's security advisory feature. Do not include secrets or scraped personal data in a report.
