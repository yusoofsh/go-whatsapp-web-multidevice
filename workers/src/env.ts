import type { OAuthHelpers } from "@cloudflare/workers-oauth-provider";
export interface Env {
  ACCOUNT: DurableObjectNamespace;
  OAUTH_KV: KVNamespace;
  OAUTH_PROVIDER: OAuthHelpers;
  ADMIN_PASSWORD_HASH: string;
  SESSION_ENCRYPTION_KEY: string;
  PUBLIC_ORIGIN?: string;
  ALLOWED_MCP_ORIGINS?: string;
  MEDIA_ALLOWED_HOSTS?: string;
  MAX_MEDIA_BYTES?: string;
  EVENT_RETENTION_DAYS?: string;
  WHATSAPP_ALLOWED_JIDS?: string;
  WEBHOOK_URL?: string;
  WEBHOOK_SECRET?: string;
  DEVELOPMENT?: string;
}
