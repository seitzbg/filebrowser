# Security and functionality fixes

This fork addresses the September 2026 review of upstream commit `833d9088`.

| Review area | Change |
| --- | --- |
| Shell hooks | Pass filenames and usernames through the process environment without interpolating them into shell source. Bound hook execution time. |
| Frontend dependencies | Update vulnerable direct dependencies and override old transitive XML, lodash, and nanoid versions. |
| User imports | Back up existing users with private permissions, import in one database transaction, reject batches that leave no administrator, and roll back failed batches. Existing-ID upserts were already supported by the database. |
| Resumable uploads | Validate lengths before opening targets, use exclusive creation for new files, serialize upload operations locally or through Redis, bind state to the uploading user and file revision, finish empty uploads immediately, and propagate cache failures. Run before hooks before touching files and surface after-hook failures. |
| Symlink rules | Check both the requested path and its resolved in-scope target, including creation through symlinked directories. |
| Secret exports | Write exports to a temporary file with mode `0600`, sync it, and atomically replace the destination. |
| Request exhaustion | Bound JSON bodies, body-read idle time, password-check concurrency, attempts per transport peer, and external authentication calls. |
| Media memory | Bound subtitle input, thumbnail input/header bytes, and decoded image pixel counts. Pass request cancellation to thumbnail generation. |
| Browser protection | Apply security headers to fallback pages, nonce application scripts, prohibit cross-origin framing, and persist sessions in scoped HttpOnly cookies. Remove legacy JWT local storage. |
| Commands | Execute the allowlisted binary directly, drain stdout/stderr together, limit command messages and output lines, and cancel on disconnect or timeout. |
| Expired shares | Filter expired entries without modifying the slice being iterated. |
| Search | Handle filesystem errors before accessing file information, prune denied directories, and keep streaming heartbeats running. |
| Administration and deployment | Serialize administrator removals/demotions, make multi-field updates transactional, reject missing user payloads, and persist `/srv`, `/database`, and `/config` in Compose. |

## Compatibility and operating limits

- Interactive commands no longer interpret shell operators through the configured shell. Administrators should allowlist specific programs. Allowlisting an interpreter still grants that interpreter's capabilities; command execution is not a filesystem sandbox.
- Shell hooks should quote environment variables, for example `process-file "$FILE"`. Hook commands have a two-minute limit; authentication hooks have a 30-second limit. Interactive commands have a five-minute limit and output lines are limited to 1 MiB.
- Each resumable request has a five-minute processing limit. A concurrent request to the same upload returns `409` and can be retried. Redis locks have a ten-minute crash-recovery lease. Redis replicas must share the same filesystem namespace; a Redis outage can interrupt uploads.
- Abandoned upload files are retained in **both** cache backends. Their upload authorization expires after three idle minutes. Clean them up explicitly; expiry no longer deletes a pathname that may now refer to another file. Upload revision checks detect changes to size or modification time before a chunk starts; upload locks do not coordinate writes through other APIs or directly on the host filesystem.
- Upgrading changes the Redis upload metadata format and ownership keys. Restart incomplete uploads after upgrading. Uploads still write directly to their destination; a valid, explicitly authorized overwrite starts by truncating the old file.
- Password login, signup, and password-protected share checks share a limit of 20 attempts per minute per client, with four concurrent password operations. By default the transport peer identifies the client. For a reverse proxy, configure its IP addresses or CIDRs with `--trustedProxies=10.0.0.2/32,2001:db8:1::/64` (also available through `config set` and the `trustedProxies` server configuration array). Only those peers may supply `X-Forwarded-For`; the chain is walked from right to left to the first untrusted address. Configure the proxy to append the actual connecting address or overwrite the header, and trust only proxy addresses. Malformed headers fall back to the peer; `Forwarded` is not used.
- Non-file request bodies are limited to 1 MiB and 15 seconds. File body reads allow 30 seconds without progress. Subtitle conversion is limited to 8 MiB. Thumbnail inputs are limited to 32 MiB, headers to 1 MiB, dimensions to 10,000 per side, and total pixels to 20 million. Thumbnail worker count still controls aggregate memory use.
- Browser cookies use `Secure` with TLS or `X-Forwarded-Proto: https`. A TLS-terminating proxy should overwrite that header. The application keeps its current JWT in memory for API headers; it no longer persists JWTs in local storage. API clients using `X-Auth` continue to work.
- Logout requires `X-Requested-With: FileBrowser` and rejects cross-site fetch metadata, including when the session has expired. The UI clears local authentication state even if the network request fails; an offline browser cannot clear the server's HttpOnly cookie until the server is reachable. The legacy bundle includes the `globalThis` polyfill so it can start with the strict CSP.
- Exports retain the secrets needed for configuration/user restoration and must remain private. Replacing users must include at least one administrator.
- Compose uses the upstream image by default. Build and select an image from this fork to run these changes; pushing source does not deploy them. Existing data under the old `/flux/vault` mount requires an explicit migration.

## Verification

Regression tests cover malicious hook filenames, expired share batches, search errors, symlink aliases, private atomic exports, transactional imports, concurrent last-admin deletion, upload length/overwrite handling, upload concurrency and replacement detection, empty uploads, cache failures, command output/allowlists, cookie flags, password limiting, and image pixel limits.

Run `go test -race ./...` and `go vet ./...`, then `pnpm --dir frontend test`, `pnpm --dir frontend lint`, `pnpm --dir frontend build`, and `pnpm --dir frontend audit --prod`.

For the Redis integration test, set `FILEBROWSER_TEST_REDIS_URL` to an isolated test Redis instance and run `go test ./http -run TestRedisUploadCoordination`. It verifies two-client locking and metadata, multi-chunk upload completion, and cache failure propagation. Browser smoke checks cover login, refresh, logout, HttpOnly storage, and CSP bootstrap under a non-root base URL.

Initial validation on 2026-09-09 passed the full Go race suite (including Redis integration), `go vet`, golangci-lint 2.13.2, all 29 frontend tests, frontend lint/typecheck/production build, Compose validation, and Chrome session/editor smoke checks. The frontend production audit reported no known vulnerabilities. `govulncheck` reported no reachable or imported-package vulnerabilities, with five advisories confined to required modules whose affected packages are not imported.

Review follow-ups add regression coverage for virtual BasePathFs rules, export cleanup, failed upload initialization, logout failure and CSRF protection, trusted proxy chain parsing, and separate per-client rate limits. Frontend tests now total 32.

Follow-up validation passed the Go race suite, `go vet`, golangci-lint, all 32 frontend tests, frontend lint/typecheck/build, and browser logout checks. Chrome exercised both the modern bundle and the forced legacy bundle with `globalThis` removed before loading: both booted under the strict CSP, rejected logout without the custom header, cleared local state after an aborted logout request, and cleared the HttpOnly cookie on successful logout.
