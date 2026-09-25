"""Exercise a fresh container's public gateway and OAuth flow without pairing WhatsApp."""
import base64
import hashlib
import json
import os
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("MCP_BASE_URL", "http://127.0.0.1:3101")
if urllib.parse.urlparse(BASE).hostname not in ("localhost", "127.0.0.1", "::1"):
    raise SystemExit("Smoke test is restricted to a fresh loopback test instance")
PUBLIC = "https://mcp-ci.example.com"
USER = "ci"
PASSWORD = "temporary-ci-password-not-a-real-account"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


opener = urllib.request.build_opener(NoRedirect)


def request(path, method="GET", body=None, headers=None):
    req = urllib.request.Request(BASE + path, data=body, method=method, headers=headers or {})
    try:
        with opener.open(req, timeout=8) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read()


for attempt in range(60):
    try:
        status, _, raw = request("/healthz")
        if status == 200:
            break
    except (OSError, TimeoutError):
        pass
    time.sleep(1)
else:
    raise RuntimeError("Gateway did not become healthy")
print("PASS native gateway readiness")
status, _, raw = request("/.well-known/oauth-authorization-server")
assert status == 200, (status, raw)
metadata = json.loads(raw)
assert metadata["code_challenge_methods_supported"] == ["S256"]
status, headers, _ = request("/mcp", "POST", b"{}", {"Content-Type": "application/json"})
assert status == 401 and "oauth-protected-resource" in headers.get("WWW-Authenticate", "")
print("PASS OAuth discovery and unauthenticated rejection")

redirect = "https://client.example.com/callback"
verifier = secrets.token_urlsafe(48)
challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
registration = {"client_name": "container-smoke", "redirect_uris": [redirect], "token_endpoint_auth_method": "none", "grant_types": ["authorization_code", "refresh_token"], "response_types": ["code"]}
status, _, raw = request("/oauth/register", "POST", json.dumps(registration).encode(), {"Content-Type": "application/json"})
assert status == 201, (status, raw)
client_id = json.loads(raw)["client_id"]
params = {"response_type": "code", "client_id": client_id, "redirect_uri": redirect, "code_challenge": challenge, "code_challenge_method": "S256", "resource": PUBLIC + "/mcp", "scope": "mcp", "state": secrets.token_urlsafe(16)}
status, _, raw = request("/oauth/authorize?" + urllib.parse.urlencode(params))
assert status == 200 and b"Authorize" in raw
params.update(username=USER, password=PASSWORD)
status, headers, raw = request("/oauth/authorize", "POST", urllib.parse.urlencode(params).encode(), {"Content-Type": "application/x-www-form-urlencoded"})
assert status in (302, 303), (status, raw)
query = urllib.parse.parse_qs(urllib.parse.urlparse(headers["Location"]).query)
assert query["state"] == [params["state"]]
status, _, raw = request("/oauth/token", "POST", urllib.parse.urlencode({"grant_type": "authorization_code", "client_id": client_id, "code": query["code"][0], "redirect_uri": redirect, "code_verifier": verifier, "resource": PUBLIC + "/mcp"}).encode(), {"Content-Type": "application/x-www-form-urlencoded"})
assert status == 200, (status, raw)
tokens = json.loads(raw)
assert tokens.get("refresh_token")
print("PASS authorization code, PKCE and token issuance")

headers = {"Authorization": "Bearer " + tokens["access_token"], "Content-Type": "application/json", "Accept": "application/json, text/event-stream"}


def rpc(method, params):
    status, returned, raw = request("/mcp", "POST", json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(), headers)
    assert status == 200, (status, raw)
    if "text/event-stream" in returned.get("Content-Type", ""):
        raw = next(line[6:] for line in raw.splitlines() if line.startswith(b"data: "))
    result = json.loads(raw)
    assert "error" not in result, result
    return result["result"], returned


_, returned = rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}})
headers["Mcp-Session-Id"] = returned["Mcp-Session-Id"]
status, _, _ = request("/mcp", "POST", b'{"jsonrpc":"2.0","method":"notifications/initialized"}', headers)
assert status == 202
result, _ = rpc("tools/list", {})
names = {tool["name"] for tool in result["tools"]}
assert {"whatsapp_chat", "whatsapp_send", "whatsapp_message", "whatsapp_media", "whatsapp_events"} <= names
chat = next(tool for tool in result["tools"] if tool["name"] == "whatsapp_chat")
assert "request_history" in chat["inputSchema"]["properties"]["action"]["enum"]
print("PASS OAuth-authenticated stateful MCP and parity tool discovery")
status, _, _ = request("/mcp", "GET", headers={"Authorization": headers["Authorization"], "Mcp-Session-Id": "unknown", "Accept": "text/event-stream"})
assert status == 404
status, _, _ = request("/mcp", "POST", b"{}", {"Authorization": "Bearer invalid", "Content-Type": "application/json"})
assert status == 401
status, _, _ = request("/mcp", "DELETE", headers=headers)
assert status in (200, 204)
print("PASS invalid tokens, unknown sessions and session cleanup")
print("SUCCESS: no WhatsApp account paired and no WhatsApp message sent")
