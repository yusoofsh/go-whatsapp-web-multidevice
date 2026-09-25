# MCP request-safety follow-up

The 11 existing tools and supported capability map are retained. No unsupported
newsletter/profile operations or voice/video call initiation/acceptance are added.

## Corrections

- Startup no longer prints the Viper settings map, which may include Basic Auth,
  webhook, database and integration credentials. Existing logs are not rewritten.
  Assess access to old logs and rotate credentials if they were exposed.
- Account/chat presence is immediate-only. Scheduling fields (`scheduled_at`,
  `timezone`, `recurrence`, `weekdays`, `day_of_month`, `end_at`,
  `occurrence_limit`) are rejected even if empty, before invoking a usecase.
  Existing text/media/status scheduling remains supported.
- Profile mutations (`update_profile`, `update_avatar`, `update_push_name`)
  reject `phone`: they can only modify the selected account. Select that account
  with `device_id`. `phone` remains supported for profile reads.
- New group-photo and participant-export actions normalize numeric group IDs to
  `@g.us`, including legacy hyphenated IDs. Individual and newsletter JIDs are
  rejected before invocation. Existing group actions retain their behavior.

## Verification boundaries

Regression tests use test doubles and isolated configuration subprocesses, not
WhatsApp sessions. Container acceptance runs against fresh loopback-only instances
and exercises OAuth refresh rotation, the complete documented tool catalog, and
unpaired device listing. Full tests, vet, race tests, compiled-schema comparison,
both native container builds and anonymous GHCR verification remain release gates.

No production deployment, live WhatsApp message or remote profile/group change is
part of this verification. Publishing an image does not update the running service
or the external Composio catalog. See the
[connector migration instructions](composio-schema-migration.md).
