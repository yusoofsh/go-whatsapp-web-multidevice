import { strict as assert } from "node:assert";
import { createHash, randomBytes } from "node:crypto";
const base = process.env.MCP_BASE_URL || "http://127.0.0.1:8899";
if (!["127.0.0.1", "localhost"].includes(new URL(base).hostname))
  throw new Error(
    "Smoke tests mutate test state and only run against loopback",
  );
const password =
  process.env.TEST_PASSWORD || "test-only-owner-password-not-for-production";
let assertions = 0;
function check(value: unknown, message: string) {
  assert.ok(value, message);
  assertions++;
  console.log("PASS " + message);
}
async function req(path: string, init: RequestInit = {}) {
  return fetch(path.startsWith("http") ? path : base + path, {
    ...init,
    redirect: "manual",
    signal: AbortSignal.timeout(12000),
  });
}
async function jsonPost(
  path: string,
  data: unknown,
  headers: Record<string, string> = {},
) {
  return req(path, {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: JSON.stringify(data),
  });
}
const health = await req("/health");
check(health.status === 200, "Worker health");
check(
  (await req("/internal/password")).status === 404,
  "Internal account routes are not public",
);
check(
  (await req("/api/status")).status === 401,
  "Admin API rejects unauthenticated requests",
);
const denied = await jsonPost("/mcp", {
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: {},
});
check(
  denied.status === 401 && !!denied.headers.get("www-authenticate"),
  "MCP requires OAuth",
);
check(
  (await jsonPost("/mcp", {}, { origin: "https://evil.example" })).status ===
    403,
  "MCP rejects untrusted browser origin",
);
const metadata = (await (
  await req("/.well-known/oauth-authorization-server")
).json()) as any;
check(
  metadata.code_challenge_methods_supported.includes("S256") &&
    !metadata.code_challenge_methods_supported.includes("plain"),
  "S256-only PKCE discovery",
);
const resource = (await (
  await req("/.well-known/oauth-protected-resource/mcp")
).json()) as any;
check(
  resource.resource === base + "/mcp",
  "Canonical protected-resource metadata",
);
const registered = await jsonPost("/oauth/register", {
  client_name: "GOWA smoke test",
  redirect_uris: ["https://client.example/callback"],
  grant_types: ["authorization_code", "refresh_token"],
  response_types: ["code"],
  token_endpoint_auth_method: "none",
});
const client = (await registered.json()) as any;
check(
  registered.status === 201 && !!client.client_id,
  "Dynamic client registration",
);
const verifier = randomBytes(48).toString("base64url"),
  challenge = createHash("sha256").update(verifier).digest("base64url");
const authorization =
  "/oauth/authorize?" +
  new URLSearchParams({
    client_id: client.client_id,
    redirect_uri: "https://client.example/callback",
    response_type: "code",
    scope: "mcp",
    state: "smoke-state",
    code_challenge: challenge,
    code_challenge_method: "S256",
    resource: base + "/mcp",
  });
const form = await req(authorization),
  html = await form.text(),
  csrf = /name="csrf" value="([^"]+)"/.exec(html)?.[1];
check(form.status === 200 && !!csrf, "Explicit OAuth approval form");
const cookies = form.headers.get("set-cookie")!.split(";")[0];
const approval = await req(authorization, {
  method: "POST",
  headers: {
    "content-type": "application/x-www-form-urlencoded",
    origin: base,
    cookie: cookies,
  },
  body: new URLSearchParams({ password, csrf: csrf! }).toString(),
});
check(approval.status === 302, "Owner password and CSRF approval");
const redirect = new URL(approval.headers.get("location")!),
  code = redirect.searchParams.get("code")!;
check(
  redirect.searchParams.get("state") === "smoke-state" && !!code,
  "OAuth state retained",
);
const exchange = await req("/oauth/token", {
  method: "POST",
  headers: { "content-type": "application/x-www-form-urlencoded" },
  body: new URLSearchParams({
    grant_type: "authorization_code",
    client_id: client.client_id,
    code,
    redirect_uri: "https://client.example/callback",
    code_verifier: verifier,
    resource: base + "/mcp",
  }).toString(),
});
const tokens = (await exchange.json()) as any;
check(
  exchange.status === 200 && !!tokens.access_token && !!tokens.refresh_token,
  "Authorization code exchange and refresh token",
);
const headers: Record<string, string> = {
  authorization: "Bearer " + tokens.access_token,
  accept: "application/json, text/event-stream",
};
let counter = 10;
async function rpc(method: string, params: unknown = {}) {
  const r = await jsonPost(
    "/mcp",
    { jsonrpc: "2.0", id: counter++, method, params },
    headers,
  );
  const body = (await r.json()) as any;
  check(r.status === 200 && !body.error, method + " RPC succeeds");
  return { r, body };
}
const initialized = await rpc("initialize", {
  protocolVersion: "2025-06-18",
  capabilities: {},
  clientInfo: { name: "gowa-smoke", version: "1" },
});
headers["mcp-session-id"] = initialized.r.headers.get("mcp-session-id")!;
headers["mcp-protocol-version"] = initialized.body.result.protocolVersion;
check(!!headers["mcp-session-id"], "Stateful MCP session");
check(
  (
    await jsonPost(
      "/mcp",
      { jsonrpc: "2.0", method: "notifications/initialized" },
      headers,
    )
  ).status === 202,
  "MCP initialization acknowledgement",
);
const tools = (await rpc("tools/list")).body.result.tools;
check(
  tools.length === 9 &&
    tools.some((t: any) => t.name === "whatsapp_history") &&
    tools.some((t: any) => t.name === "whatsapp_media"),
  "History and attachment tools are exposed",
);
async function tool(name: string, args: unknown = {}) {
  return (await rpc("tools/call", { name, arguments: args })).body.result;
}
check(
  (await tool("whatsapp_status")).structuredContent.data.status ===
    "disconnected",
  "Fresh test account is not connected",
);
check(
  (await tool("whatsapp_chat", { action: "list_chats" })).structuredContent.data
    .length === 0,
  "Stored chat query",
);
check(
  (
    await tool("whatsapp_chat", {
      action: "get_messages",
      chat_jid: "status@broadcast",
    })
  ).isError,
  "Invalid JIDs rejected by tools",
);
const bytes = Buffer.from("%PDF-1.7\nGOWA attachment transport test\n");
const staged = await tool("whatsapp_media", {
  data_base64: bytes.toString("base64"),
  mime_type: "application/pdf",
  filename: "smoke.pdf",
});
check(
  !staged.isError && !!staged.structuredContent.data.media_id,
  "Stage attachment without sending",
);
const media = (
  await rpc("resources/read", { uri: staged.structuredContent.data.uri })
).body.result.contents[0];
check(
  media.mimeType === "application/pdf" &&
    Buffer.from(media.blob, "base64").equals(bytes),
  "MCP binary attachment round-trip",
);
check(
  (
    await tool("whatsapp_media", {
      data_base64: bytes.toString("base64"),
      mime_type: "text/html",
      filename: "x.html",
    })
  ).isError,
  "Active HTML media is rejected",
);
await rpc("resources/subscribe", { uri: "whatsapp://events" });
const controller = new AbortController(),
  timer = setTimeout(() => controller.abort(), 20000);
const streamPromise = fetch(base + "/mcp", {
  headers: { ...headers, accept: "text/event-stream" },
  signal: controller.signal,
});
await new Promise((r) => setTimeout(r, 100));
// Safe test mutation: a fresh, unpaired local account is disconnected. No WhatsApp traffic.
await tool("whatsapp_disconnect", { logout: false });
const stream = await streamPromise,
  reader = stream.body!.getReader();
let text = "";
try {
  while (!text.includes("notifications/resources/updated")) {
    const part = await reader.read();
    if (part.done) break;
    text += new TextDecoder().decode(part.value);
  }
  check(
    text.includes("notifications/resources/updated"),
    "Realtime MCP resource notification",
  );
} finally {
  await reader.cancel();
  controller.abort();
  clearTimeout(timer);
}
const journal = (await tool("whatsapp_events", { after_cursor: 0 }))
  .structuredContent.data;
check(
  journal.events.some((e: any) => e.topic === "connection") &&
    journal.next_cursor > 0,
  "Durable event journal and cursor",
);
check(
  (await tool("whatsapp_events", { after_cursor: journal.next_cursor }))
    .structuredContent.data.events.length === 0,
  "Cursor replay does not duplicate consumed events",
);
const adminForm = await req("/"),
  adminHtml = await adminForm.text(),
  adminCsrf = /name="csrf" value="([^"]+)"/.exec(adminHtml)![1];
const admin = await req("/", {
  method: "POST",
  headers: {
    "content-type": "application/x-www-form-urlencoded",
    origin: base,
    cookie: adminForm.headers.get("set-cookie")!.split(";")[0],
  },
  body: new URLSearchParams({ password, csrf: adminCsrf }).toString(),
});
check(admin.status === 303, "Owner dashboard login");
const sessionCookie = admin.headers.get("set-cookie")!.split(";")[0];
check(
  (await req("/api/status", { headers: { cookie: sessionCookie } })).status ===
    200,
  "Authenticated dashboard status",
);
check(
  (
    await req("/api/connect", {
      method: "POST",
      headers: { cookie: sessionCookie, origin: "https://evil.example" },
    })
  ).status === 403,
  "Dashboard writes enforce same origin",
);
const invalid = await jsonPost(
  "/mcp",
  { jsonrpc: "2.0", id: 99, method: "tools/list", params: {} },
  { ...headers, "mcp-session-id": "unknown" },
);
check(invalid.status === 404, "Unknown MCP sessions require reinitialization");
check(
  (await req("/mcp", { method: "DELETE", headers })).status === 200,
  "MCP session cleanup",
);
console.log(
  `SUCCESS: ${assertions} integration assertions. No WhatsApp account was paired and no messages were sent.`,
);
