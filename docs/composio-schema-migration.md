# custom_gowa schema migration

The external Composio `custom_gowa` registration/configuration is not maintained in this repository. This change does not edit a live connection, switch its endpoint, or claim its cache has refreshed. The source of truth is the compiled server's authenticated `tools/list`, exported verbatim as [`mcp-tools.json`](mcp-tools.json) by the validation job.

Apply the following changes to the external catalog after deploying the tested image, using that JSON's complete `inputSchema` and `description` for each named tool. Preserve all existing fields and action values; do not flatten away conditional requirements.

| Tool | Exact additions |
|---|---|
| `whatsapp_send` | Add `type` enum values `status`, `presence`, `chat_presence`. Add `status_type` (`text`, `image`, `video`), `presence` (`available`, `unavailable`), `chat_presence` (`start`, `stop`). `phone` remains required for every original type and chat presence, but not account presence or status. Status uses fixed `status@broadcast`; its text/media conditional requirements are in the exported schema. Keep existing `media_id` and media-source exclusivity. |
| `whatsapp_chat` | Add `action=pin`, `unpin`, `set_disappearing`; `pinned` boolean; `timer_seconds` integer enum 0, 86400, 604800, 7776000. Preserve `get_messages`, `archive`, and `request_history` with `count` 1..500. |
| `whatsapp_group` | Add `action=get_photo`, `set_photo`, `remove_photo`, `export_participants`; staged `media_id`, bounded `limit`/`offset`, optional `inline`. |
| `whatsapp_app` | Add `action=list_devices`, `add_device`, `remove_device`, `reject_call`; `target_device_id`, `caller_jid`, `call_id`, bounded `limit`/`offset`. `target_device_id` is a management target, not the per-call WhatsApp data scope. |
| `whatsapp_history` | Register new tool. Actions `search_all`, `context`, `export`, `coverage`, `request_backfill`. Fields: `device_id`, `chat_jid`, `message_id`, `sender`, `search`, `start_time`, `end_time`, `media_only`, `media_type`, `message_type`, `is_from_me`, `limit`, `offset`, `before`, `after`, `count`, `inline`. Use exact bounds and conditional requirements from JSON. |
| `whatsapp_newsletter` | Register new tool. Actions `list`, `get_messages`, `download_media`, `unfollow`; `device_id`, `newsletter_id`, `count`, `before`, `server_id`, `limit`, `offset`, `inline`. `before` is an exclusive message server-ID cursor. |
| `whatsapp_profile` | Register new tool. Actions `get_profile`, `get_avatar`, `update_avatar`, `update_push_name`, `update_profile`, `get_privacy`, `get_business_profile`, `check_number`; `device_id`, `phone`, `media_id`, `push_name`, `is_preview`, `is_community`. `update_profile` changes exactly one of push name or avatar. No privacy/business write actions. |
| `whatsapp_message` | Keep existing distinct `delete` and `revoke`; preserve `inline` download option. Correct stale descriptions: MCP-enabled downloads return private resources/bytes, not a server-local path. |
| `whatsapp_media`, `whatsapp_events` | Preserve PR #2 additions. Register them if the existing catalog still contains only the original five tools. |

Expected native catalog: **10 tools**, retaining the five original names. External toolkit slugs such as `CUSTOM_GOWA_*` are assigned by Composio, not hard-coded here. Refresh/discover the catalog using the external service's supported process after the endpoint is deployed; reconnect only when that service requires it. Do not assume publishing code refreshes a cached schema automatically.

Every tool continues to accept per-call `device_id` where account data is involved. Header `X-Device-Id` is the default. Native resource subscriptions remain bound to the session's default device; overrides apply to individual tool calls only. See the capability map for authentication and administrative-scope limits.

For gateways that keep `structuredContent` but drop image/resource blocks, use `whatsapp_media` action `read`, same device scope, `include_data=true`. Do not label a file read or parsed merely because a resource URI was returned.
