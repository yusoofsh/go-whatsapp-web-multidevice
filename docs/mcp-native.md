# Native GoWA MCP on main

This fork's `main` contains the native Go/Whatsmeow MCP enhancements and the upstream v9.5.0 changes, including scheduling and upstream commit `831a851e677f48e25aa671170daade586f798551`. It is not the separate TypeScript/Baileys Cloudflare Workers experiment. PR #2 has been merged; subsequent capability-parity work is on `main`.

The authoritative action inventory is [capability-map.md](capability-map.md). Exact compiled tool schemas are exported to [mcp-tools.json](mcp-tools.json), with external connector instructions in [composio-schema-migration.md](composio-schema-migration.md).

## Container images and validation

```text
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:latest
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:mcp
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:mcp-parity
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:sha-<full-commit-sha>
```

`latest`, `mcp` and `mcp-parity` now follow successful **main** builds; the old feature branch no longer moves these aliases. Prefer a manifest digest for immutable deployment. GitHub Actions runs the full Go suite, vet, MCP/archive race checks, a native build and a semantic check that the documented tool schemas match the compiled server. It then builds and pulls Linux amd64 and ARM64 images and tests each on its native runner with a fresh OAuth/MCP HTTP handshake. Shared tags are published only after both architecture checks pass.

The publish job additionally uses an **anonymous** GHCR token to verify the index, both architecture manifests and image-config revision labels. It does not use the Actions credential for this public-access check. Publication uses the job-scoped `GITHUB_TOKEN` with `packages:write`, not a PAT or Docker Hub secret. Actions are pinned to commits. Registry publication does not deploy a running WhatsApp instance.

## Deploy

```bash
git clone --branch main https://github.com/yusoofsh/go-whatsapp-web-multidevice.git
cd go-whatsapp-web-multidevice
cp docker/mcp.env.example docker/mcp.env
# Edit docker/mcp.env: set APP_BASIC_AUTH and GOWA_PUBLIC_URL.
docker compose --env-file docker/mcp.env -f docker/compose.mcp.yml pull
docker compose --env-file docker/mcp.env -f docker/compose.mcp.yml up -d
```

Use a long unique password without `:` because the inherited Basic Auth parser expects `username:password`. Keep the real env file out of Git. Set `GOWA_PUBLIC_URL` to your public HTTPS origin without a trailing slash. The example Compose file enables OAuth and streaming and persists `/app/storages` and `/app/statics` in named volumes. Back up existing volumes before changing images; do not run two processes against one WhatsApp session/SQLite database.

Point the HTTPS reverse proxy to **port 3001**, not 3000:

```text
MCP client -> HTTPS reverse proxy -> native gateway :3001
                                     |-- /mcp: stateful Streamable HTTP
                                     `-- OAuth, REST, UI -> internal Fiber :3000
```

Compose publishes only `127.0.0.1:3001`. A containerized reverse proxy should use a private Docker network and target `gowa:3001`. Disable proxy buffering for `/mcp` and allow streaming GET requests. Do not expose port 3000 simply to enable streaming. Do not put an interactive bot challenge or another login in front of OAuth metadata or the MCP endpoint. Terminate TLS at the reverse proxy and rate-limit failed owner logins there.

Open the UI and pair normally when you are ready for live acceptance. The connector URL is `https://<host>/mcp`. Existing OAuth includes S256 PKCE, dynamic registration, resource-bound access tokens and rotating refresh tokens; no extra identity provider is required. See [mcp-oauth.md](mcp-oauth.md) for base-path configuration.

### Configuration

| Variable | Default / purpose |
|---|---|
| `MCP_ENABLED` | `true`; native tools and private data store. |
| `MCP_STREAMING_ENABLED` | `false` in ordinary GoWA; `true` in this MCP image. |
| `MCP_STREAM_PORT` | `3001`; must differ from `APP_PORT`. |
| `MCP_DATA_DIR` | `storages/mcp`; private event/attachment storage. |
| `MCP_OAUTH_ENABLED` | `false` ordinarily; enabled by Compose. |
| `MCP_OAUTH_ISSUER_URL` | Public HTTPS issuer; supplied from `GOWA_PUBLIC_URL` by Compose. |
| `MCP_OAUTH_RESOURCE_URL` | Canonical public `/mcp` endpoint; derived unless explicitly set. |

CLI flags override environment configuration. `APP_BASE_PATH` still applies. For Basic-only clients, configure exact public origins through `APP_CORS_ALLOWED_ORIGINS`; the native gateway does not accept wildcard origins. `/healthz` is process/transport liveness only, not proof of WhatsApp pairing, connectivity or complete history.

## Eleven consolidated tools

The original names `whatsapp_send`, `whatsapp_message`, `whatsapp_chat`, `whatsapp_group` and `whatsapp_app` remain. The catalog also contains upstream `whatsapp_schedule`, plus `whatsapp_media`, `whatsapp_events`, `whatsapp_history`, `whatsapp_newsletter` and `whatsapp_profile`.

Existing per-chat `get_messages` remains available. On SQLite, its search now combines date, media, direction and offset filters rather than bypassing them. Message delete-for-me and revoke-for-everyone remain distinct. New profile and newsletter actions expose existing GoWA usecases; unsupported writes are explicitly listed in the capability map. Presence actions operate immediately; scheduling support belongs to message sends and the upstream scheduling tool.

### Archive workflows

`whatsapp_history` provides `search_all`, `context`, `export`, `coverage`, and `request_backfill`. It reuses the existing message table and indexes, not a second archive database. Searches accept chat, sender, time, media, stored message-type and direction filters. Text matching is literal SQL substring matching, not WACLI FTS5 syntax. Empty stored media type maps to `text`; richer historical message types are not fabricated.

Search/export pages are 1–500 rows with offsets 0–100,000. Context windows are 0–100 rows before and after an exact chat/message identity. Time bounds are inclusive; a date-only bound means midnight UTC. Search is newest-first, export oldest-first, with stable chat/message tie breakers for equal timestamps. Pages are separate snapshots: ingestion can change later pages. Use fixed time bounds and deduplicate by chat/message ID when exporting. Narrow filters after the maximum offset.

Coverage reports locally stored counts, oldest/newest timestamps and a usable backfill anchor. It does **not** prove complete phone history. `request_backfill`, and the preserved `whatsapp_chat.action=request_history`, call the existing per-chat history request with count 1–500. A `requested` result means dispatched, not completed. Retrieval is asynchronous and best effort; query after a `history.sync` event. An empty chat has no anchor, and the phone/WhatsApp determines historical availability.

Exports are bounded JSON files in the private attachment store. Their DTOs exclude media encryption keys, CDN access material and raw protocol data. Participant exports use the same private file mechanism. Files over 10 MiB are rejected; reduce the page limit or narrow filters.

### Attachments through MCP

Stage bytes without public hosting:

```json
{"name":"whatsapp_media","arguments":{"action":"upload","filename":"report.pdf","mime_type":"application/pdf","data_base64":"JVBERi0xLjc="}}
```

The response contains `media_id`, `uri`, byte size, SHA-256 and expiry; staging sends nothing. Send the returned `media_id` with `whatsapp_send` for documents, images, video, audio or stickers. Supply exactly one source: a staged ID or its corresponding URL. The native implementation retains upstream FFmpeg/media processing. Staged uploads are tested against the actual production validators as well as fake send clients.

`whatsapp_message.action=download_media` downloads into private storage and returns metadata plus an inline image or embedded binary resource. `inline=false` omits inline bytes. Newsletter downloads follow the same private import path. Read `whatsapp://media/<id>` with `resources/read`, or use the tool fallback:

```json
{"name":"whatsapp_media","arguments":{"action":"read","media_id":"<id>","include_data":true}}
```

The fallback includes `data_base64` in structured output for gateways that discard binary blocks. A resource URI alone does not prove that a client downloaded or understood a file. Large base64 responses consume client context.

Limits: **10 MiB per file**, **24-hour staging expiry**, **256 MiB / 1,024 staged files globally**. Invalid base64, traversal/control characters in filenames, spoofed images, and active HTML/XML/SVG/script MIME types are rejected. OOXML documents are supported. Staging is not permanent backup storage.

### Realtime events

Call `whatsapp_events` with a cursor and bounded limit (1–500). Persist `next_cursor` and continue while `has_more` is true. Events contain device/chat/message references, type, timestamp and journal ID—not message bodies or encryption keys.

Native clients may subscribe to `whatsapp://events` using `resources/subscribe` and an authenticated GET SSE stream at `/mcp`. The server emits `notifications/resources/updated` after a journal commit. Notifications are hints; read the durable cursor stream for recovery. Current events cover messages, sent-message persistence, edits, revocations, reactions, receipts, delete-for-me, history batches, connection changes and group updates. Existing HTTP webhooks remain separate and have a broader payload surface.

Retention is seven days / approximately 100,000 events. `cursor_expired` means rescan state; older events cannot be replayed. Process idempotently because protocol redelivery can repeat message references. Message storage and the event journal are not one cross-database transaction, so this is not a guaranteed lossless protocol audit feed.

Native sessions are memory-resident, capped at 128 and expire after ten idle minutes. Restart requires reinitialization; journal/media data persist. SSE streams close after 55 seconds to require reauthentication. Reconnect and use the durable cursor; a separate `Last-Event-ID` replay store is not implemented. Client support is required; notifications do not automatically wake ChatGPT or start a background agent.

## Device and storage safety

Every native request, including GET, authenticates. Session IDs are not credentials. Sessions bind the authenticated principal and default `X-Device-Id`; stolen cross-principal sessions or changing a session's default header device are rejected.

For **individual tool calls**, explicit `device_id` overrides the header/default device without changing the session or its subscriptions. Resource reads and SSE subscriptions remain scoped to the session's default account. Read an overridden account's file with `whatsapp_media.read` and the same `device_id`, or initialize a separate session. Unpaired devices cannot read private attachments or subscribe to account events.

Credentials remain administrative, not per-user account ACLs. Device list/add/remove uses explicit `target_device_id` and does not require a selected paired account. Do not share owner credentials with untrusted users.

Private files remain outside public `statics`, including symlink checks. Protect volumes and backups: the SQLite data is not independently encrypted at rest. The entrypoint fixes named-volume ownership and runs the app non-root. The reaction identity repair uses a separate named fork-migration ledger so future upstream numbered migrations do not collide. Its transactional table rebuild preserves existing rows; already-overwritten historical data cannot be recovered from absence.

## Verification limits

Tests use fake WhatsApp clients and temporary databases. They cover schemas, validation, pagination, errors, device scoping, production DTO validation, archive filters/context/coverage, migration preservation, byte round-trips, and real local HTTP/SSE with synthetic events. Container checks exercise actual OAuth registration, authorization code/PKCE, tokens and authenticated MCP discovery without pairing an account.

**No real WhatsApp messages were sent and no production deployment was modified.** Live phone history recovery, message/media delivery, newsletter permissions and long-running reliability still require acceptance testing. Voice/video call initiation and acceptance are intentionally absent; only rejection of an existing incoming call is exposed. The external Composio catalog is not automatically updated by a code/image publication; follow the schema migration guide after deployment.
