import { OAuthProvider } from "@cloudflare/workers-oauth-provider";
import type { Env } from "./env";
import { dashboard, page } from "./pages";
import {
  cookie,
  equal,
  escapeHtml,
  hmac,
  json,
  readBounded,
  sha256,
} from "./util";
export { WhatsAppAccount } from "./account";
const OWNER = "owner";
function stub(env: Env) {
  return env.ACCOUNT.get(env.ACCOUNT.idFromName(OWNER));
}
async function internal(env: Env, path: string, data: unknown): Promise<any> {
  const r = await stub(env).fetch(`https://account.internal/internal/${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(data),
  });
  if (!r.ok) throw new Error("Account service failed");
  return r.json();
}
function local(request: Request, env: Env) {
  const u = new URL(request.url);
  return (
    env.DEVELOPMENT === "true" &&
    ["localhost", "127.0.0.1"].includes(u.hostname) &&
    u.protocol === "http:"
  );
}
function cookieName(name: string, request: Request, env: Env) {
  return `${local(request, env) ? "" : "__Host-"}gowa_${name}`;
}
function setCookie(
  name: string,
  value: string,
  request: Request,
  env: Env,
  age: number,
) {
  return `${cookieName(name, request, env)}=${value}; Path=/; HttpOnly; SameSite=Strict; Max-Age=${age}${local(request, env) ? "" : "; Secure"}`;
}
function sameOrigin(request: Request) {
  return request.headers.get("origin") === new URL(request.url).origin;
}
async function limited(
  request: Request,
  env: Env,
  kind: string,
): Promise<boolean> {
  return (
    await internal(env, "limit", {
      kind,
      ip: await sha256(request.headers.get("cf-connecting-ip") || "local"),
    })
  ).allowed;
}
async function authenticated(request: Request, env: Env): Promise<boolean> {
  const token = cookie(request, cookieName("session", request, env));
  return !!token && (await internal(env, "session", { token })).valid;
}
async function formPage(
  request: Request,
  env: Env,
  title: string,
  details: string,
  error = "",
  status = 200,
): Promise<Response> {
  const nonce = crypto.randomUUID(),
    stamp = `${Date.now()}.${crypto.randomUUID()}`,
    csrf = `${stamp}.${await hmac(`csrf:${request.url}:${stamp}`, env.SESSION_ENCRYPTION_KEY)}`;
  const response = page(
    title,
    `${details}${error ? `<p class="error" role="alert">${escapeHtml(error)}</p>` : ""}<form method="post"><input type="hidden" name="csrf" value="${csrf}"><label for="password">Owner password</label><input id="password" name="password" type="password" required maxlength="1024" autocomplete="current-password"><p><button type="submit">${title === "Authorize WhatsApp access" ? "Authorize" : "Sign in"}</button></p></form>`,
    nonce,
  );
  const headers = new Headers(response.headers);
  headers.append("set-cookie", setCookie("csrf", csrf, request, env, 300));
  return new Response(response.body, { status, headers });
}
async function formPassword(request: Request, env: Env): Promise<string> {
  if (!sameOrigin(request)) throw new Error("Origin validation failed");
  if (
    !request.headers
      .get("content-type")
      ?.startsWith("application/x-www-form-urlencoded")
  )
    throw new Error("Expected form submission");
  const form = new URLSearchParams(
      (await readBounded(request, 8192)).toString(),
    ),
    csrf = form.get("csrf") || "",
    saved = cookie(request, cookieName("csrf", request, env));
  const [time, nonce, signature] = csrf.split(".");
  if (
    !csrf ||
    !equal(csrf, saved) ||
    !/^\d+$/.test(time) ||
    Date.now() - Number(time) > 300000 ||
    Number(time) > Date.now() + 1000 ||
    !nonce ||
    !signature
  )
    throw new Error("Expired or invalid form");
  if (
    !equal(
      signature,
      await hmac(
        `csrf:${request.url}:${time}.${nonce}`,
        env.SESSION_ENCRYPTION_KEY,
      ),
    )
  )
    throw new Error("Invalid form signature");
  return form.get("password") || "";
}
const defaultHandler: ExportedHandler<Env> = {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/health" && request.method === "GET")
      return json({
        ok: true,
        service: "gowa-workers",
        engine: "baileys",
        version: "0.1.0",
      });
    if (url.pathname === "/oauth/authorize") {
      if (!["GET", "POST"].includes(request.method))
        return json({ error: "Method not allowed" }, 405);
      let auth;
      try {
        auth = await env.OAUTH_PROVIDER.parseAuthRequest(
          new Request(request.url, { method: "GET" }),
        );
      } catch {
        return json({ error: "Invalid OAuth authorization request" }, 400);
      }
      if (auth.scope.some((scope) => scope !== "mcp"))
        return json({ error: "Unsupported scope" }, 400);
      const client = await env.OAUTH_PROVIDER.lookupClient(auth.clientId);
      if (!client) return json({ error: "Unknown OAuth client" }, 400);
      const details = `<p><strong>${escapeHtml(client.clientName || "MCP client")}</strong> requests access to your WhatsApp account.</p><p>Approving permits reading chats and attachments, requesting history, sending messages and files, and modifying your own messages. This authorizes one owner account—not multi-user access.</p><p>Redirect: <code>${escapeHtml(auth.redirectUri)}</code></p>`;
      if (request.method === "GET")
        return formPage(request, env, "Authorize WhatsApp access", details);
      if (!(await limited(request, env, "login")))
        return json({ error: "Too many authentication attempts" }, 429);
      try {
        const password = await formPassword(request, env);
        if (!(await internal(env, "password", { password })).valid)
          return formPage(
            request,
            env,
            "Authorize WhatsApp access",
            details,
            "Incorrect password",
            401,
          );
        const { redirectTo } = await env.OAUTH_PROVIDER.completeAuthorization({
          request: auth,
          userId: OWNER,
          scope: ["mcp"],
          metadata: { clientName: client.clientName || "MCP client" },
          props: { userId: OWNER, scopes: ["mcp"] },
        });
        return new Response(null, {
          status: 302,
          headers: {
            location: redirectTo,
            "cache-control": "no-store",
            "set-cookie": setCookie("csrf", "", request, env, 0),
          },
        });
      } catch {
        return formPage(
          request,
          env,
          "Authorize WhatsApp access",
          details,
          "Invalid or expired form. Reload and try again.",
          400,
        );
      }
    }
    if (url.pathname === "/" || url.pathname === "/admin") {
      if (request.method === "GET")
        return (await authenticated(request, env))
          ? dashboard(crypto.randomUUID())
          : formPage(
              request,
              env,
              "Sign in",
              "<p>Enter the owner password to pair and manage your WhatsApp linked device.</p>",
            );
      if (request.method !== "POST")
        return json({ error: "Method not allowed" }, 405);
      if (!(await limited(request, env, "login")))
        return json({ error: "Too many authentication attempts" }, 429);
      try {
        const password = await formPassword(request, env);
        if (!(await internal(env, "password", { password })).valid)
          return formPage(
            request,
            env,
            "Sign in",
            "",
            "Incorrect password",
            401,
          );
        const { token } = await internal(env, "session", { create: true });
        return new Response(null, {
          status: 303,
          headers: {
            location: "/admin",
            "cache-control": "no-store",
            "set-cookie": setCookie("session", token, request, env, 3600),
          },
        });
      } catch {
        return formPage(
          request,
          env,
          "Sign in",
          "",
          "Invalid or expired form. Reload and try again.",
          400,
        );
      }
    }
    if (url.pathname === "/logout" && request.method === "POST") {
      if (!sameOrigin(request) || !(await authenticated(request, env)))
        return json({ error: "Not authorized" }, 401);
      await internal(env, "session", {
        token: cookie(request, cookieName("session", request, env)),
        remove: true,
      });
      return new Response(null, {
        status: 204,
        headers: {
          "set-cookie": setCookie("session", "", request, env, 0),
          "cache-control": "no-store",
        },
      });
    }
    if (
      [
        "/api/status",
        "/api/connect",
        "/api/disconnect",
        "/api/logout",
        "/api/events",
      ].includes(url.pathname)
    ) {
      if (!(await authenticated(request, env)))
        return json({ error: "Sign in first" }, 401);
      if (request.method !== "GET" && !sameOrigin(request))
        return json({ error: "Origin validation failed" }, 403);
      return stub(env).fetch(request);
    }
    return json({ error: "Not found" }, 404);
  },
};
export default {
  async fetch(
    request: Request,
    env: Env,
    ctx: ExecutionContext,
  ): Promise<Response> {
    try {
      const url = new URL(request.url),
        origin = env.PUBLIC_ORIGIN || url.origin;
      if (
        new URL(origin).origin !== origin ||
        url.origin !== origin ||
        (!local(request, env) && url.protocol !== "https:")
      )
        return json({ error: "Invalid public origin" }, 400);
      if (!env.ADMIN_PASSWORD_HASH || !env.SESSION_ENCRYPTION_KEY)
        return json({ error: "Required deployment secrets are missing" }, 503);
      if (!!env.WEBHOOK_URL !== !!env.WEBHOOK_SECRET)
        return json({ error: "Configure both webhook URL and secret" }, 503);
      if (request.method === "POST" && url.pathname !== "/mcp") {
        try {
          const bytes = await readBounded(request, 32768);
          request = new Request(request, { body: bytes });
        } catch {
          return json({ error: "Request too large" }, 413);
        }
      }
      if (
        url.pathname === "/oauth/register" &&
        request.method === "POST" &&
        !(await limited(request, env, "register"))
      )
        return json({ error: "Registration rate limit exceeded" }, 429);
      if (url.pathname === "/mcp") {
        const allowed = [
            origin,
            ...(env.ALLOWED_MCP_ORIGINS || "")
              .split(",")
              .map((s) => s.trim())
              .filter(Boolean),
          ],
          sent = request.headers.get("origin");
        if (sent && !allowed.includes(sent))
          return json({ error: "MCP origin not allowed" }, 403);
      }
      const provider = new OAuthProvider<Env>({
        apiRoute: "/mcp",
        apiHandler: {
          async fetch(req, bindings, context) {
            const props = (
              context as ExecutionContext & {
                props?: { userId?: string; scopes?: string[] };
              }
            ).props;
            if (new URL(req.url).pathname !== "/mcp")
              return json({ error: "Not found" }, 404);
            if (props?.userId !== OWNER || !props.scopes?.includes("mcp"))
              return json({ error: "Not authorized" }, 403);
            return stub(bindings).fetch(req);
          },
        },
        defaultHandler,
        authorizeEndpoint: "/oauth/authorize",
        tokenEndpoint: "/oauth/token",
        clientRegistrationEndpoint: "/oauth/register",
        allowPlainPKCE: false,
        clientIdMetadataDocumentEnabled: true,
        scopesSupported: ["mcp"],
        accessTokenTTL: 3600,
        refreshTokenTTL: 2592000,
        resourceMetadata: {
          resource: `${origin}/mcp`,
          ...(local(request, env) ? {} : { authorization_servers: [origin] }),
          scopes_supported: ["mcp"],
          resource_name: "GOWA WhatsApp",
          bearer_methods_supported: ["header"],
        },
      });
      return await provider.fetch(request, env, ctx);
    } catch (error) {
      console.error(
        "Worker request failed",
        error instanceof Error ? error.name : "Error",
      );
      if (local(request, env)) console.error(error);
      return json({ error: "Request failed" }, 500);
    }
  },
} satisfies ExportedHandler<Env>;
