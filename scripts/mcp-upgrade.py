"""Final one-time integration: expose non-sensitive gateway process health."""
from pathlib import Path
p=Path('src/cmd/mcp_runtime.go')
s=p.read_text()
if 'r.URL.Path == "/healthz"' not in s:
    old='if r.URL.Path == route {'
    assert s.count(old)==1
    s=s.replace(old,'''if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
            w.Header().Set("Content-Type","application/json")
            w.Header().Set("Cache-Control","no-store")
            _, _ = w.Write([]byte(`{"status":"ok","transport":"streamable-http"}`))
            return
        }
        '''+old,1)
p.write_text(s)
assert 'isInitialize := false' in Path('src/ui/mcp/native.go').read_text()
assert 'GetMessageByIDChatAndDevice(deviceID, dataWaRecipient.String(), request.MessageID)' in Path('src/usecase/message.go').read_text()
print('Private media, GET session ownership and process health are integrated.')
