"""One-time guard patch. Applied and committed by the temporary integration job."""
from pathlib import Path
p=Path('src/ui/mcp/native.go')
s=p.read_text()
if 'isInitialize := false' not in s:
    old='if r.Method == http.MethodPost {'
    assert s.count(old)==1
    s=s.replace(old,'isInitialize := false\n\t'+old,1)
    old='if envelope.Method == "initialize" {'
    assert s.count(old)==1
    s=s.replace(old,'isInitialize = envelope.Method == "initialize"\n\t\t\t'+old,1)
    old='if r.Method == http.MethodGet {'
    assert s.count(old)==1
    s=s.replace(old,'''// The SDK does not validate session IDs on GET. Enforce ownership here
    // for every non-initialize method, including GET, before opening a stream.
    if !isInitialize {
        terminated, err := h.ResolveSessionIdManager(r).Validate(r.Header.Get("Mcp-Session-Id"))
        if err != nil || terminated {http.Error(w,"invalid MCP session",http.StatusNotFound);return}
    }
    '''+old,1)
p.write_text(s)
print('Native session ownership is enforced on POST, GET and DELETE.')
