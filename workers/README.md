# GOWA on Cloudflare Workers

**Experimental native Workers port, not a reverse proxy and not a full GOWA REST replacement.**

The upstream Go GOWA application remains unchanged in `src/`. This `workers/` application replaces Go/Whatsmeow with Baileys and runs the WhatsApp connection inside a SQLite-backed Durable Object. It does not require a VPS, Cloudflare Containers, R2, or the WhatsApp Business Cloud API.

The OAuth architecture is informed by [rahilp/second-brain-cloudflare](https://github.com/rahilp/second-brain-cloudflare), using Cloudflare's maintained OAuth provider. This is an independently implemented single-owner service, not a copy of that project's memory database.

## Architecture

```text
ChatGPT / another MCP client
          |
     HTTPS + OAuth 2.1
          |
Cloudflare Worker -- OAuth KV
          |
One SQLite Durable Object
  |- Baileys + native outbound WebSocket -> WhatsApp
  |- encrypted protocol/session keys
  |- chats, messages, contacts and stored history
  |- chunked attachment storage
  |- durable event journal + webhook outbox
  `- stateful MCP sessions and resource notifications
```

No WhatsApp connection starts at deployment. The owner explicitly connects and scans a QR code. Use one deployment for one owner and one WhatsApp account. Do not share the owner password with unrelated users: a granted OAuth client can access the entire account.

## Included MCP surface

| Tool | Behavior |
|---|---|
| `whatsapp_status` | Connection/pairing state |
| `whatsapp_connect` | Start or reconnect the linked device |
| `whatsapp_disconnect` | Disconnect; optional explicit unlink |
| `whatsapp_chat` | Paginated chats, contacts and stored messages |
| `whatsapp_history` | Request older messages from the phone using a stored anchor |
| `whatsapp_events` | Replay durable events using `after_cursor` |
| `whatsapp_media` | Stage base64 attachments without sending |
| `whatsapp_send` | Text, ready-encoded image/video/audio/document/sticker, contact, location, poll, forwarding |
| `whatsapp_message` | Download media, react, edit/revoke your own messages, mark read |

MCP resources:

- `whatsapp://events`: subscribable resource; changes produce `notifications/resources/updated`.
- `whatsapp://media/{id}`: attachment bytes through `resources/read`, with MIME type and base64 `blob`. Downloaded images are also returned inline in tool results.

A URL is not required for outgoing attachments: use `data_base64`, or reuse a staged `media_id`. Each send requires `idempotency_key`; a repeated key cannot silently resend an operation with an unknown outcome. This is not an exactly-once guarantee for the WhatsApp network.

## Deploy

Prerequisites: Bun 1.4.2, Node.js 22, and a Cloudflare account. Do not upgrade dependency versions without rerunning the Workers-specific tests.

```sh
git clone https://github.com/yusoofsh/go-whatsapp-web-multidevice.git
cd go-whatsapp-web-multidevice
git switch feat/cloudflare-workers
cd workers
bun install --frozen-lockfile
bun run typecheck
bun test
bun run build
bun x wrangler login
```

Generate secrets locally with a unique password of at least 16 characters. The password itself is not stored by the service. Keep the generated encryption key permanently: changing it without migrating the stored protocol keys makes the WhatsApp session unreadable.

```sh
# Avoid placing your password in shell history.
read -rs -p 'Owner password: ' GOWA_PASSWORD; echo
export GOWA_PASSWORD
bun run secrets:generate
unset GOWA_PASSWORD
```

Copy the two generated values to Wrangler's interactive secret prompts. Do not commit them, paste them into public issues, or include them in ordinary environment variables in `wrangler.jsonc`.

```sh
bun x wrangler secret put ADMIN_PASSWORD_HASH
bun x wrangler secret put SESSION_ENCRYPTION_KEY
bun run deploy
```

Wrangler can provision the missing `OAUTH_KV` namespace from the binding declaration. If your deployment workflow asks for an existing namespace, create one with `bun x wrangler kv namespace create OAUTH_KV` and add the returned `id` to that binding. The Durable Object SQLite migration is part of `wrangler.jsonc`.

For Cloudflare Workers Builds connected to this fork, use root directory `workers`, install command `bun install --frozen-lockfile`, build command `bun run build`, and deploy command `bun run deploy`. Configure both required secrets in the Cloudflare dashboard. No GitHub-hosted workflow has deployment credentials by default.

After deployment, open the Worker URL, sign in, choose **Connect / Pair**, and scan the QR code from WhatsApp's linked-device settings. Keep the page open while pairing. In a custom MCP client/app, use `https://<your-worker>/mcp` and OAuth. Discovery, S256 PKCE, Dynamic Client Registration, Client ID Metadata Documents, refresh tokens and explicit owner consent are provided by `@cloudflare/workers-oauth-provider`.

A ChatGPT plugin package is not required. Tool execution, binary-resource rendering and notification handling still depend on the MCP client's supported capabilities. An MCP notification does not automatically wake ChatGPT or cause a new autonomous agent run.

## Free-tier constraints

The configuration uses resources available on Workers Free, but **this is not unlimited free hosting**. Limits are shared with other applications in the Cloudflare account. See the current primary references:

- [Durable Objects pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/): SQLite-backed objects are available on Free; currently 100,000 requests/day, 13,000 GB-s/day, 100,000 SQLite rows written/day and 5 GB total stored SQL data.
- [Workers limits](https://developers.cloudflare.com/workers/platform/limits/): the front Worker has its own request, CPU, memory and subrequest limits.
- [Workers KV limits](https://developers.cloudflare.com/kv/platform/limits/): OAuth registration, grants and token refreshes consume KV operations.
- [Containers pricing](https://developers.cloudflare.com/containers/platform/pricing/): Containers require a paid plan, so this implementation deliberately does not use them.

An outbound WhatsApp WebSocket prevents hibernation. One continuously active object accounts for roughly 11,000 GB-s/day at the documented allocation, leaving limited free-duration headroom for other objects. It is deliberately one object, not one object per MCP session. More accounts, large history imports, many active OAuth clients or heavy attachment traffic can exceed free allowances. On a Free plan, operations fail when their limits are exhausted; do not treat quota errors as a complete sync.

Attachments default to **2 MiB each**, stored in 256 KiB SQL chunks. `MAX_MEDIA_BYTES` can be set between 1 KiB and 8 MiB, but the MCP request-body cap remains 3 MiB; raising a media limit does not make large base64 uploads fit. History and attachment records are not automatically erased. Event retention defaults to seven days; expired event cursors require reconciliation from stored messages.

## Configuration

| Setting | Purpose |
|---|---|
| `PUBLIC_ORIGIN` | Optional fixed HTTPS origin without trailing slash; otherwise the request origin is used |
| `ALLOWED_MCP_ORIGINS` | Exact comma-separated browser origins allowed to call MCP; absent Origin is allowed for server-side clients |
| `WHATSAPP_ALLOWED_JIDS` | Optional outgoing recipient allowlist |
| `MEDIA_ALLOWED_HOSTS` | Exact HTTPS hostnames allowed for remote media URLs; empty disables URL uploads |
| `MAX_MEDIA_BYTES` | Default 2097152 |
| `EVENT_RETENTION_DAYS` | Default 7, range 1–90 |
| `WEBHOOK_URL` | Optional fixed public HTTPS destination |
| `WEBHOOK_SECRET` | Required with a webhook URL; configure as a Worker secret |

Outgoing media URL redirects are not followed. The `global_fetch_strictly_public` compatibility flag adds Cloudflare's public-network restriction. HTML and SVG uploads are rejected; other binary documents must still be treated as untrusted.

Webhooks carry a stable `x-gowa-event-id` and `x-gowa-signature-256: sha256=<HMAC-SHA256(raw-body, secret)>`. Verify the signature before processing and deduplicate by event ID. Delivery is at least once while attempts remain: failed requests retry with backoff up to six attempts. A bounded batch is retried by alarms. Expired event retention also expires corresponding delivery records.

## Compatibility and limitations

- **Different engine and session format.** Existing GOWA/Whatsmeow session databases are not imported. Back up the original deployment and pair this as a new linked device. Leave the original available until you have verified the new deployment.
- **Not a complete lifetime archive.** Initial sync and on-demand backfill depend on history the phone/WhatsApp supplies. Deleted, unavailable or expired messages/media cannot be recreated. Pagination only enumerates locally stored records.
- **Not full REST/UI parity.** GOWA's existing REST routes, multi-account administration, Chatwoot integration, newsletter management and all specialized group/admin features are not ported in this first version. Use upstream Go GOWA when those are required.
- **No native image/audio processing.** No FFmpeg or sharp executables are available. Supply compatible encoded files; voice-note formatting is not arbitrary audio transcoding. Thumbnail generation is bypassed.
- **Realtime requires client support.** MCP GET/SSE streams can deliver resource-change notifications. Sessions are in memory, expire and can be lost on deployment/eviction. Reinitialize after a 404 and resume with a durable event cursor. There is no guarantee that a disconnected client or ChatGPT will consume a push.
- **A bridge can break.** Baileys is an unofficial WhatsApp client; this port does not eliminate protocol changes, disconnections, account restrictions or bans. Do not use it for spam or assume it is suitable for a business-critical number.
- **Live verification is separate.** Unit, type and runtime protocol checks are not proof of successful production pairing, history backfill or media delivery. Test with a non-critical account before replacing an existing deployment.

## Tests and local development

Create a local `.dev.vars` containing the two generated secrets. `bun run dev` explicitly enables loopback HTTP for local development; production requires HTTPS. The development override does not weaken production OAuth issuer validation.

```sh
bun test
bun run typecheck
bun run build
bun run dev
```

The GitHub **Workers** workflow runs a fresh local workerd instance and `scripts/smoke.ts`, covering OAuth discovery/approval/token exchange, stateful MCP initialization, access boundaries, file-resource round-trips and SSE event notifications. It uses a disposable test password and never pairs WhatsApp or sends a real message. `bun run test:integration` expects that fresh test instance; do not point it at your own populated deployment.

The WhatsApp Rust bridge currently embeds WASM in JavaScript. `scripts/prepare-native.ts` verifies the exact dependency checksum, extracts the non-SIMD module during the build, and lets Wrangler statically compile it. Dependency upgrades intentionally fail that check until reviewed; no protocol encryption implementation has been replaced with a mock.

## Security

OAuth protects `/mcp`; owner session cookies protect the dashboard. Password verification uses PBKDF2-SHA256, and protocol/session keys are encrypted with AES-GCM before entering SQLite. Browser forms enforce signed, expiring CSRF tokens and same-origin writes. Login and registration are rate-limited. Chat and attachment content is not separately application-encrypted: Cloudflare storage security and your account access controls remain important.

Back up your encryption key privately. Do not commit `.dev.vars`, `.wrangler`, OAuth data, session keys, QR codes or downloaded media. Keep the Worker account and OAuth grants private. Revoke unneeded clients and use an outgoing recipient allowlist while testing.
