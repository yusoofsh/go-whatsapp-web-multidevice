"""Apply narrowly asserted source integrations, then let CI test before committing.
No WhatsApp runtime is started; all acceptance calls use mocks or fresh containers.
"""
from pathlib import Path
import shutil

root=Path('.')
def replace(path,old,new,count=1):
 p=root/path;s=p.read_text()
 actual=s.count(old)
 if actual!=count:raise RuntimeError(f'{path}: expected {count} exact integration anchors, found {actual}')
 p.write_text(s.replace(old,new,count))

for source in sorted(Path('scripts/parity-source').rglob('*.txt')):
 target=Path(str(source.relative_to('scripts/parity-source'))[:-4])
 if target.exists():raise RuntimeError(f'refusing to overwrite an unexpected existing source: {target}')
 target.parent.mkdir(parents=True,exist_ok=True)
 shutil.copyfile(source,target)

replace('src/domains/chatstorage/archive.go',' Asc bool\n',' Asc bool\n // Preserve the legacy per-chat offset contract; not exposed by the archive tool.\n AllowLargeOffset bool\n')
replace('src/domains/chatstorage/archive.go','f.Offset > ArchiveMaxOffset','(f.Offset > ArchiveMaxOffset && !f.AllowLargeOffset)')

replace('src/ui/mcp/server.go','domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"','domainApp "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/app"\n domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"\n domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"\n domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"')
replace('src/ui/mcp/server.go','type Deps struct {','type Deps struct {\n Device domainDevice.IDeviceUsecase\n Newsletter domainNewsletter.INewsletterUsecase\n Call domainCall.ICallUsecase')
replace('src/ui/mcp/server.go','\treturn s\n','\tregisterParityTools(s, deps, resolver)\n\treturn s\n')
replace('src/cmd/mcp_runtime.go','Group: groupUsecase}', 'Group: groupUsecase, Device: deviceUsecase, Newsletter: newsletterUsecase, Call: callUsecase}')

p=Path('src/ui/mcp/native.go');s=p.read_text()
start=s.index('\t\t\tif envelope.Method == "tools/call" {')
end=s.index('\t\t\tif envelope.Method == "resources/subscribe"',start)
assert 'native MCP sessions are device-bound' in s[start:end]
s=s[:start]+'''            // Tool-level device_id overrides the header default through
            // resolveDeviceContext. It does not mutate the authenticated
            // session identity used for GET/resources/subscriptions.
'''+s[end:]
p.write_text(s)

for path,var,needle in [
 ('src/ui/mcp/native_test.go','result','map[string]any{"name": "whatsapp_events", "arguments": map[string]any{"device_id": "b"}})'),
 ('src/ui/mcp/native_regression_test.go','obj','map[string]any{"name": "whatsapp_events", "arguments": map[string]any{"device_id": "b"}})'),
]:
 p=Path(path);s=p.read_text();start=s.index(needle);old=f'require.NotNil(t, {var}["error"])';pos=s.index(old,start)
 assert pos-start<150
 s=s[:pos]+f'require.Nil(t, {var}["error"])\n require.NotEqual(t, true, {var}["result"].(map[string]any)["isError"])'+s[pos+len(old):]
 p.write_text(s)

replace('src/domains/newsletter/newsletter.go','type DownloadMediaRequest struct {','type DownloadMediaRequest struct {\n MCPPrivate bool `json:"-" form:"-" query:"-"`')
replace('src/usecase/newsletter.go','"context"','"context"\n "crypto/sha256"')
replace('src/usecase/newsletter.go','dateDir := filepath.Join(config.PathMedia, newsletterDir, message.Timestamp.Format("2006-01-02"))','''root := config.PathMedia
 if request.MCPPrivate {
  deviceID := deviceIDFromContext(ctx)
  if deviceID == "" { return response, fmt.Errorf("device required for private newsletter download") }
  if sized, ok := downloadable.(interface{GetFileLength() uint64}); ok && sized.GetFileLength() > 10<<20 { return response, fmt.Errorf("MCP attachment exceeds 10 MiB") }
  scope := sha256.Sum256([]byte(deviceID))
  root = filepath.Join(config.McpDataDir, "downloads", fmt.Sprintf("%x", scope[:16]))
 }
 dateDir := filepath.Join(root, newsletterDir, message.Timestamp.Format("2006-01-02"))''')

p=Path('src/usecase/chat.go');s=p.read_text()
start=s.index('\t// Get messages from storage\n')
end=s.index('\t// Convert entities to domain objects\n',start)
assert 'SearchMessages(deviceID, request.ChatJID, request.Search, request.Limit)' in s[start:end]
s=s[:start]+'''    // One scoped query combines text, time, media, direction and pagination.
    filter.DeviceID = deviceID
    messages, totalCount, err := service.queryStoredChatMessages(ctx, filter, request.Search)
    if err != nil { return response, err }

'''+s[end:]
p.write_text(s)
replace('src/ui/mcp/schemas.go','download_media returns the local file path','download_media returns private MCP attachment metadata and bytes when MCP data storage is enabled')

p=Path('docs/mcp-native.md');s=p.read_text()
s=s.replace('The five existing consolidated tools remain. Two tools are added, for seven in\nthis branch.', 'The five original consolidated names remain. The native server now registers ten\ntools: those five plus media, events, history, newsletter and profile.')
s=s.replace('Cross-principal session reuse and cross-device session\nreuse are rejected. Select a device with `X-Device-Id`; reconnect to switch\ndevices. An explicit conflicting tool `device_id` cannot change a session\'s\nidentity.', 'Cross-principal session reuse and changing the default header device of an existing\nsession are rejected. Per-call `device_id` overrides are supported for tools without\nchanging the default resource/SSE subscription identity. Use a separate session\nwhen switching a subscription to another account.')
s+='\n## Archive and REST capability expansion\n\nSee [capability-map.md](capability-map.md) for the complete audited surface,\n[composio-schema-migration.md](composio-schema-migration.md) for external catalog\nchanges, and [mcp-tools.json](mcp-tools.json) for generated exact input schemas.\n'
p.write_text(s)
p=Path('docs/openapi.yaml');s=p.read_text();assert '\nx-mcp-capabilities:' not in s
s+='''
# MCP additions reuse the documented REST usecases; archive tools have no new REST routes.
x-mcp-capabilities:
  endpoint: /mcp
  transport: Streamable HTTP
  native_gateway_port: 3001
  schemas: ./mcp-tools.json
  capability_map: ./capability-map.md
  external_catalog_migration: ./composio-schema-migration.md
  account_selection: X-Device-Id default with per-tool device_id override
  live_whatsapp_verification: false
'''
p.write_text(s)

p=Path('.github/workflows/mcp-ghcr.yml');s=p.read_text()
old='if [[ "$GITHUB_REF" == refs/heads/main ]]; then tags+=(-t "$image:latest"); fi'
assert s.count(old)==1
s=s.replace(old,'if [[ "$GITHUB_REF" == refs/heads/main ]]; then tags+=(-t "$image:latest" -t "$image:mcp" -t "$image:mcp-parity"); fi')
p.write_text(s)
p=Path('scripts/mcp-container-smoke.py');s=p.read_text()
needle='print("PASS OAuth-authenticated stateful MCP and parity tool discovery")'
assert s.count(needle)==1
s=s.replace(needle,'''assert {"whatsapp_history", "whatsapp_profile", "whatsapp_newsletter"} <= names
assert len(names) == 10
history = next(tool for tool in result["tools"] if tool["name"] == "whatsapp_history")
assert set(history["inputSchema"]["properties"]["action"]["enum"]) == {"search_all", "context", "export", "coverage", "request_backfill"}
'''+needle)
p.write_text(s)
print('Applied asserted integrations. Tests must pass before source is committed.')
