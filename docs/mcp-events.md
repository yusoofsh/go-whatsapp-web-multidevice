# MCP Events

Implements authenticated `events/list`, `events/subscribe`, and `events/unsubscribe` on the MCP endpoint with `server/discover` advertising `capabilities.events` when configured. Reference-only event payloads use Standard Webhooks HMAC-SHA256 signatures, fresh delivery timestamps, stable event IDs, HTTPS callback challenge verification, finite leases, and bounded retries. A 410 response removes a subscription; 413 and permanent client errors stop retries. Duplicate delivery is possible; receivers must deduplicate `eventId`.

Callbacks must use public HTTPS on port 443. Every attempt resolves DNS afresh, rejects private/special destinations and connects to the validated address while retaining TLS hostname verification. Redirects are never followed. Callback receipts are limited to 16 KiB; event bodies to 256 KiB. Subscriptions and pending delivery credentials are encrypted at rest. Retrieve full content using the existing authenticated read tools.

## Events and configuration

Enable `MCP_EVENTS_ENABLED=true` on the native authenticated MCP listener. `MCP_EVENTS_STATE_PATH` defaults to `storages/mcp-events.enc`; its adjacent `.key` is generated privately and must be persisted/backed up with the ciphertext. An initialization error disables the capability. The Fiber adapter does not expose events.

Events cover message created/edited/revoked/reaction/receipt/deleted, connection connected/disconnected/logged-out, and group updated. All require string `device_id` matching the selected paired device's bare JID; optional `chat_id` filters further. Payloads contain only `device_id`, `chat_id`, `message_id`. The existing journal provides replay via decimal string cursors. Expired replay cursors advance to the retained watermark and set `truncated:true` on subscribe.

Subscriptions persist encrypted original authentication and device binding. Every delivery reauthenticates and confirms the device is still paired to that scope. Lease maximum: five minutes so refresh supplies a current OAuth access token. Journal scans and callback retries are bounded. Native discovery and ordinary modern tool calls are sessionless; legacy streaming remains available.

## Verification and rollout

Local tests cover the event contract with test callback receipts, temporary durable stores, and relevant authenticated MCP transports. These checks do not establish live ChatGPT subscription delivery. After deployment, rescan the plugin, create a subscription, receive and validate the callback, verify refresh/unsubscribe/revocation, restart the service and confirm recovery. Keep activation disabled until runtime configuration is present.

Reference: https://developers.openai.com/plugins/build/mcp-events
