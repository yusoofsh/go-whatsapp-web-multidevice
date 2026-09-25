# Native GOWA MCP: history, attachments and realtime events

This branch extends the **Go/Whatsmeow GOWA v9.4.0 baseline**. It preserves the
existing REST API, OAuth login, UI and WhatsApp engine. It is **not** the
TypeScript/Baileys Cloudflare Workers experiment. The original `main` and
`feat/cloudflare-workers` branches are not changed by this feature.

## Container image

GitHub Actions runs all Go tests, vet and MCP/store race tests before building
native Linux amd64 and arm64 images. Each architecture is pulled and exercised
with an OAuth + MCP HTTP smoke test. Only after both pass does the workflow
publish the combined manifest:

```text
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:mcp-parity
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:mcp
ghcr.io/yusoofsh/go-whatsapp-web-multidevice:sha-<full-commit-sha>
```

The branch tags move; pin an immutable commit tag or manifest digest in a
production deployment. `latest` is reserved for a build from `main`; this
feature branch does not replace it. First-time GHCR packages can be private.
If anonymous pulls are denied, authenticate with `docker login ghcr.io` using a
GitHub credential with package-read access, or change the package visibility
explicitly in GitHub package settings. No registry credentials go in Compose.

The workflow uses the job-scoped `GITHUB_TOKEN` with `packages: write`. It does
not require a PAT or Docker Hub secret. Actions are pinned to commit SHAs.

## Deploy

Check out this branch, then:

```bash
cp docker/mcp.env.example docker/mcp.env
# Edit docker/mcp.env: set APP_BASIC_AUTH and your public HTTPS origin.
docker compose --env-file docker/mcp.env -f docker/compose.mcp.yml pull
docker compose --env-file docker/mcp.env -f docker/compose.mcp.yml up -d
```

Use a strong unique password. The inherited Basic Auth parser expects exactly
`username:password`; choose a password without `:`. Keep the real env file out
of version control. The supplied example contains no production credential.

Forward the public HTTPS hostname to **127.0.0.1:3001**, not port 3000. The native
HTTP gateway handles `/mcp` directly and proxies OAuth discovery/login/token,
REST and UI requests to the existing internal Fiber listener. Thus one hostname
serves the complete connector flow:

```text
MCP client -> HTTPS reverse proxy -> native gateway :3001
                                     |-- /mcp: stateful Streamable HTTP
                                     `-- OAuth, REST, UI -> Fiber :3000
```

The Compose file publishes only the loopback gateway port. Do not make port
3000 public merely to enable streaming. For a containerized reverse proxy,
connect it on a private Docker network and target `gowa:3001` instead.
Disable reverse-proxy response buffering for `/mcp` and allow streaming GET
requests. Do not place an interactive bot challenge or unrelated login in front
of OAuth metadata or the MCP endpoint. Terminate TLS at the reverse proxy.

Open the UI and pair a WhatsApp linked device normally. Add the public
`https://<host>/mcp` endpoint to an OAuth-capable MCP client. OAuth remains the
existing GOWA implementation with S256 PKCE, dynamic registration, resource-bound
access tokens and rotating refresh tokens; no extra identity provider is needed.

### Configuration

| Variable | Default | Purpose |
|---|---|---|
| `MCP_ENABLED` | `true` | Enables the MCP tools and private data store. |
| `MCP_STREAMING_ENABLED` | `false` in normal GOWA; `true` in the MCP image | Enables the native HTTP gateway. |
| `MCP_STREAM_PORT` | `3001` | Gateway listener; must differ from `APP_PORT`. |
| `MCP_DATA_DIR` | `storages/mcp` | Private event/attachment database and temporary downloads. |
| `MCP_OAUTH_ENABLED` | `false`; enabled by Compose | OAuth discovery and authentication. |
| `MCP_OAUTH_ISSUER_URL` | Required for OAuth | Public HTTPS issuer, not an internal container URL. |
| `MCP_OAUTH_RESOURCE_URL` | Derived from issuer/base path | Canonical public MCP endpoint. |

Existing CLI flags override their corresponding environment values. `APP_BASE_PATH`
continues to apply; set the issuer/resource paths accordingly as described in
[mcp-oauth.md](mcp-oauth.md). For Basic-only remote clients, explicitly configure
allowed public origins through `APP_CORS_ALLOWED_ORIGINS`; wildcard origins are
not accepted by the native gateway.

`/healthz` is a process/transport liveness check only. A healthy result does not
mean that WhatsApp is paired, connected or has complete history.

## MCP feature surface

The five original consolidated names remain. The native server now registers eleven
tools: the original five plus scheduling, media, events, history, newsletter and profile. This is feature coverage, not a claim that every upstream REST
endpoint has been turned into an MCP action.

### Request older history

```json
{"name":"whatsapp_chat","arguments":{"action":"request_history","chat_jid":"628123456789@s.whatsapp.net","count":50}}
```

`count` is 1–500. The existing usecase anchors the request at the oldest stored
message for that chat/device, excluding synthetic call rows. `requested` means
the request was sent, **not** that history is complete. A successful incoming
history batch emits `history.sync`; then query `get_messages` again. A chat with
no local anchor, disabled chat storage, or an unavailable phone cannot be
backfilled through this call. WhatsApp controls historical availability.

### Upload and send a file without public hosting

```json
{"name":"whatsapp_media","arguments":{"action":"upload","filename":"report.pdf","mime_type":"application/pdf","data_base64":"JVBERi0xLjc="}}
```

The response contains `media_id`, `uri`, size, SHA-256 and expiry. This call only
stages data and sends nothing to WhatsApp. Use the returned ID:

```json
{"name":"whatsapp_send","arguments":{"type":"document","phone":"628123456789","media_id":"<returned-id>","caption":"Report"}}
```

Images, video, audio and stickers also accept `media_id`. The MIME must match
the selected send type. Supply **one** source: a staged ID or the existing media
URL field, not both. The Go implementation retains upstream media processing
such as FFmpeg; this is not the restricted Workers runtime.

Limits: 10 MiB per staged file, 24-hour expiry, 256 MiB total staged bytes and
1,024 attachments per server. Attachments are not a permanent backup. Paths and
control characters in filenames, active HTML/XML/SVG/script media, invalid
base64 and oversized payloads are rejected. OOXML documents (`docx`, `xlsx`,
`pptx`) are accepted; their MIME names are not confused with active XML.

### Download and read bytes within MCP

Use `whatsapp_message` with `action=download_media`, `phone` and `message_id`.
With MCP data enabled, the download is imported into the private attachment
store and its temporary copy is removed. The result returns media metadata and,
by default, an inline image or embedded binary resource—not a local server path.
Use `inline=false` to return metadata without inline bytes.

Read the returned `whatsapp://media/<media_id>` through `resources/read` to obtain
`mimeType` and base64 `blob`. Clients that do not support resource reads can call:

```json
{"name":"whatsapp_media","arguments":{"action":"read","media_id":"<returned-id>","include_data":true}}
```

`include_data` explicitly includes base64 in `structuredContent` for gateways
that discard binary content blocks. Large blobs still consume client context;
prefer native resource/file handling when supported. A path or resource URI
alone is not evidence that a client downloaded, parsed or understood the file.

### Events and notifications

```json
{"name":"whatsapp_events","arguments":{"cursor":0,"limit":100}}
```

Persist `next_cursor`; subsequent calls return events strictly after it.
`has_more` means request another page. Events contain only device/chat/message
references, event type, timestamp and a monotonic journal ID. Message content
and WhatsApp encryption keys are not stored in the event journal.

The native endpoint advertises resource subscriptions. Subscribe to
`whatsapp://events` using `resources/subscribe` and open an authenticated GET SSE
stream at `/mcp`. After a journal commit, the server sends
`notifications/resources/updated`. Read `whatsapp_events` to retrieve the actual
entries. Notifications are hints; the persisted journal is the recovery source.

Current events include messages, sent-message persistence, edits, revocations,
reactions, receipts, delete-for-me, successful history batches, connection state
and group changes. They are not a complete replacement for all REST webhook
payloads. Existing outbound webhooks remain available separately.

The journal retains seven days and approximately 100,000 entries. If
`cursor_expired` is true, rescan affected chat/history state; events outside
retention cannot be replayed. Consumers should process idempotently. Protocol
redelivery may produce another event referencing the same message.

Sessions are memory-resident, bound to authenticated principal and selected
paired-device identity, capped at 128, and expire after ten idle minutes.
A restart requires MCP reinitialization, but journal and attachment data persist.
SSE streams close after 55 seconds to require reauthentication; reconnect with
the same valid session ID and use the event cursor to recover notifications.
This does not implement a separate `Last-Event-ID` notification replay store.

## Security and operations

Authentication is checked on every native request, including GET. Session IDs
are not credentials. Cross-principal session reuse and changing the default header device of an existing
session are rejected. Per-call `device_id` overrides are supported for tools without
changing the default resource/SSE subscription identity. Use a separate session
when switching a subscription to another account. Unpaired devices cannot read attachments or subscribe to account data.

The inherited GOWA credentials are administrative credentials. Device scoping
prevents accidental cross-device data access; it does **not** create a new
per-user account-access policy. Do not share owner credentials with untrusted
users. Apply failed-login rate limits at the HTTPS reverse proxy.

Private data must remain outside `statics`, including through symlinks. The MCP
SQLite file is created with restricted permissions; protect the storage volume
and backups. The database is not independently encrypted at rest. The container
entrypoint fixes named-volume ownership, then runs the app as non-root.

Use a single GOWA process for a given WhatsApp session and its SQLite volumes.
This feature does not implement a distributed event bus or multi-replica session
coordination. Preserve existing volumes and take a backup before switching
images; do not copy active session databases casually between running clients.

## Verification and limitations

Automated tests use fake WhatsApp clients and temporary databases. They exercise
history dispatch/validation, private binary round-trips, staged-file sends to
stubs, device isolation, event durability/retention, authenticated real HTTP SSE,
origin/host checks, session ownership and cancellation. Container smoke tests
exercise the compiled image's public gateway and actual local OAuth code/PKCE/
token flow without pairing or messaging a real account.

Passing these tests is **not** live WhatsApp acceptance testing. Pairing,
real send/receive, historical recovery, CDN media availability and long-running
production reliability need validation with an authorized non-critical account.
Neither this fork nor stock GOWA can guarantee complete lifetime history.
MCP notification support depends on the client and does not automatically start
a ChatGPT conversation or trigger a background agent.

## Archive and REST capability expansion

See [capability-map.md](capability-map.md) for the complete audited surface,
[composio-schema-migration.md](composio-schema-migration.md) for external catalog
changes, and [mcp-tools.json](mcp-tools.json) for generated exact input schemas.
