# GoWA / WACLI capability map

## Evidence and scope

Comparison baseline: GoWA v9.4.0 (`c5871ab2fda4224bff03f35953ff2c3b1e123877`) and WACLI v0.19.0. The original GoWA baseline pins whatsmeow `v0.0.0-20260915211301-f376da267f95`. Subsequent upstream merges are recorded in Git history; this is native Go/Whatsmeow, not the separate Cloudflare Workers experiment.

**MCP-exposed** means registered in the compiled server and covered by schema/dispatch tests. **Mock verified** uses fake clients or temporary SQLite; it is not real WhatsApp end-to-end verification. No account was paired, messages sent, or production state changed during these checks. Nothing below is labelled live **end-to-end verified**.

| Capability | REST exposure in baseline | MCP exposure | Verification / limit |
|---|---|---|---|
| Cross-chat text search with chat, sender, date, media, direction and stored-type filters | No dedicated cross-chat route | MCP-exposed: `whatsapp_history.search_all` | Real temporary SQLite; combined filters, exact device scope, stable ordering and pagination tested. |
| Before/after message context | No dedicated context route | MCP-exposed: `whatsapp_history.context` | Full `(device, chat, message)` identity; equal-time tie breaking tested. |
| Bounded JSON archive export | No dedicated archive export route | MCP-exposed: `whatsapp_history.export` | Private MCP attachment; plaintext DTO excludes media encryption keys and CDN tokens. |
| Local coverage | No dedicated coverage route | MCP-exposed: `whatsapp_history.coverage` | Count, media count, oldest/newest timestamps and oldest real backfill anchor. Completeness remains unknown. |
| Older history request | REST-exposed: per-chat history request | MCP-exposed: `whatsapp_history.request_backfill`; existing `whatsapp_chat.request_history` retained | Existing usecase; asynchronous/best-effort. Mock dispatch and invalid bounds tested. |
| Per-chat message queries | REST-exposed: chat messages | MCP-exposed: existing `whatsapp_chat.get_messages` | Search now retains date/media/direction/offset filters with filtered total on production SQLite. Reactions and response shape retained. |
| Text/image/video status broadcasts | REST-exposed through existing generic send operations to `status@broadcast` (not a separate status endpoint) | MCP-exposed: `whatsapp_send` type `status`, `status_type=text/image/video` | Pinned whatsmeow `broadcast.go` explicitly resolves status privacy recipients. Mock dispatch only; no real broadcast sent. |
| Account presence | REST-exposed: send presence | MCP-exposed: `whatsapp_send` type `presence` | `presence=available/unavailable`; mock mapping tested. |
| Chat presence | REST-exposed: send chat-presence | MCP-exposed: `whatsapp_send` type `chat_presence` | `chat_presence=start/stop`; mock mapping tested. |
| Pin/unpin | REST-exposed: chat pin | MCP-exposed: `whatsapp_chat.pin/unpin` | Existing PinChat usecase; pin also accepts `pinned=false`. |
| Disappearing timer | REST-exposed: chat disappearing settings | MCP-exposed: `whatsapp_chat.set_disappearing` | `timer_seconds=0/86400/604800/7776000`; invalid values rejected. |
| Subscribed newsletter list | REST-exposed: own newsletter list | MCP-exposed: `whatsapp_newsletter.list` | Bounded returned page; underlying baseline API fetches subscribed list. |
| Newsletter messages | REST-exposed: newsletter messages | MCP-exposed: `whatsapp_newsletter.get_messages` | Count 1..100, exclusive server-ID `before`; returns `next_before`, not an invented full-history guarantee. |
| Newsletter media | REST-exposed: newsletter media download | MCP-exposed: `whatsapp_newsletter.download_media` | MCP-only private download path; inline image/blob and private resource, no public file path. Mock download/import verified. |
| Unfollow newsletter | REST-exposed | MCP-exposed: `whatsapp_newsletter.unfollow` | Existing Unfollow usecase, mock mapping. |
| Newsletter follow/join or publish | No dedicated pinned GoWA REST usecase | Unsupported in this MCP extension | SDK primitives alone are not treated as a complete GoWA operation. `unfollow` is the supported leave operation. |
| Group photo get/set/remove | REST-exposed through avatar and group-photo usecases | MCP-exposed: `whatsapp_group.get_photo/set_photo/remove_photo` | Set uses staged private `media_id`; nil-photo removal is explicitly supported by baseline validation. |
| Participant export | REST-exposed through participant query/export adapter | MCP-exposed: `whatsapp_group.export_participants` | Bounded JSON, private resource. Existing `participants` action unchanged. |
| Device list/add/remove | REST-exposed: device management | MCP-exposed: `whatsapp_app.list_devices/add_device/remove_device` | Works without a selected/paired device for management. Uses explicit `target_device_id`, not a data-scope override. |
| Profile info/avatar | REST-exposed | MCP-exposed: `whatsapp_profile.get_profile/get_avatar` | Phone defaults to selected account; mock mapping. |
| Own profile updates | REST-exposed: avatar/push-name updates | MCP-exposed: `whatsapp_profile.update_avatar/update_push_name/update_profile` | `update_profile` accepts exactly one of `push_name` or staged `media_id`; avoids partially applied multi-write requests. |
| Privacy/business-profile reads | REST-exposed | MCP-exposed: `whatsapp_profile.get_privacy/get_business_profile` | Only fields supplied by baseline usecases. |
| Privacy/business-profile/about writes | No supported pinned GoWA REST usecase | Unsupported | No invented generic profile setter. |
| Delete-for-me vs revoke-for-everyone | REST-exposed separately | MCP-exposed: existing `whatsapp_message.delete/revoke` | Original distinct meanings preserved. |
| Incoming-call rejection | REST-exposed: RejectCall | MCP-exposed: `whatsapp_app.reject_call` | Requires existing `caller_jid` and `call_id`; fake client only. |
| Voice/video initiate or accept | Not implemented | Unsupported, intentionally excluded | Unknown action rejected by schema. |
| Private attachments and event cursors | Existing PR #2 extension | MCP-exposed: `whatsapp_media`, `whatsapp_events`, resource subscriptions | Existing HTTP/SSE, OAuth and binary regression suite retained. |

## Archive semantics

No second message database or FTS migration is introduced. Queries reuse GoWA's `messages` table and existing device index. Text matching is a literal, case-insensitive SQL LIKE substring (SQLite's existing case-folding limits apply), not WACLI FTS5 query syntax. `%`, `_` and backslash are escaped in the new archive search. Stored media type defines `message_type`; empty media type is `text`, as in the WACLI comparison's basic text/media filter. Missing original payloads or richer historical classifications are not invented. Status rows follow GoWA's storage model rather than WACLI's separate status table.

`search_all` and `export`: limit 1..500, offset 0..100000, inclusive time bounds. A date-only bound means midnight UTC, not an implicit end of day. Search is newest first; exports are oldest first. Equal timestamps use chat JID and message ID as stable tie breakers. Pagination is a bounded snapshot per request, not a frozen archive across multiple requests; ingestion can change later pages. Use a fixed time window when exporting, and deduplicate by chat+message ID. Use narrower filters after the maximum archive offset. The legacy per-chat API retains its existing offset range.

Context windows are 0..100 before and after, ordered chronologically. Coverage describes only locally stored data; an empty chat has no backfill anchor. Existing synthetic call records are excluded as backfill anchors. Exports are capped at 10 MiB and reuse the 24-hour private staging store; reduce the limit when large messages exceed the cap. This is not a permanent backup service.

## Device compatibility and subscriptions

For tools, explicit `device_id` overrides the device selected by `X-Device-Id`, as before. Native session authentication still binds the principal and default header device. A per-call override creates only a scoped child context; it never retargets an existing SSE subscription or a subsequent resource read. Resources/subscriptions continue to use the session's default device. To read bytes for an overridden device, call `whatsapp_media` with that same `device_id`, or initialize a separate session for it. Cross-principal session reuse and changing the header device of an existing session remain rejected.

Credentials remain administrative, not per-user device ACLs. Device management changes must not be shared with untrusted users. There is no voice/video initiation or acceptance. MCP notifications still require client support and do not start autonomous conversations.

## Verification

The integration gate runs the full Go suite, vet, race tests and native build, then exports the actual registered schemas to `mcp-tools.json`. Existing GHCR publication separately tests both native architectures and the real OAuth/PKCE/MCP HTTP handshake. Mock dispatch is distinguished from live WhatsApp acceptance; live delivery, phone history, channel permissions and long-running reliability are unverified.

See `composio-schema-migration.md` for exact connector changes. Existing deployments and the external `custom_gowa` catalog are not altered merely by publishing a new image.

## Reaction identity repair

The existing reaction primary key omitted chat identity. An appended transactional migration preserves existing rows and adds chat JID to the key; reaction updates and incoming removal events now target the full chat/device identity. New collision and migration-preservation tests cover this correction. No historical overwritten reactions can be reconstructed from absent data.
