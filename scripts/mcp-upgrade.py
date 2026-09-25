"""One-time integration patch for the MCP branch; removed after it is committed.
Every replacement asserts its expected source so unrelated changes cannot be lost.
"""
from pathlib import Path
import re


def edit(path, fn):
    p = Path(path)
    old = p.read_text()
    new = fn(old)
    p.write_text(new)


def replace(s, old, new):
    if new in s:
        return s
    if s.count(old) != 1:
        raise ValueError(f"Expected one source match: {old[:100]!r}")
    return s.replace(old, new, 1)


def rest(s):
    s = replace(s, "loadMcpOAuthEnvConfig()", """loadMcpOAuthEnvConfig()
    loadMcpRuntimeConfig()
    if err:=initializeMcpData();err!=nil{logrus.Fatalln("MCP data initialization: ",err)}
    defer func(){whatsapp.SetMCPEventStore(nil);if mcpData!=nil{_ = mcpData.Close();mcpData=nil}}()""")
    s = re.sub(r"uimcp.Register\(apiGroup, dm, uimcp.Deps\{.*?\n\s*\}\)", "uimcp.Register(apiGroup, dm, runtimeMcpDeps())", s, count=1, flags=re.S)
    return replace(s, "\tgo websocket.RunHub()", """    stopMCP,err:=startNativeMcpGateway(dm,oauthServer)
    if err!=nil{logrus.Fatalln("Native MCP startup: ",err)}
    defer stopMCP()
    go websocket.RunHub()""")


edit("src/cmd/rest.go", rest)
edit("src/cmd/mcp_oauth.go", lambda s: re.sub(r"uimcp.Register\(mcpRouter, dm, uimcp.Deps\{.*?\n\s*\}\)", "uimcp.Register(mcpRouter, dm, runtimeMcpDeps())", s, count=1, flags=re.S))
edit("src/infrastructure/whatsapp/event_handler.go", lambda s: replace(s,
    "ctx = ContextWithDevice(ctx, instance)",
    """ctx = ContextWithDevice(ctx, instance)
    eventDevice := instance.JID()
    defer func(){
        if eventDevice=="" {eventDevice=instance.JID()}
        recordMCPProtocolEvent(ctx,eventDevice,rawEvt)
    }()"""))
edit("src/infrastructure/whatsapp/history_sync.go", lambda s: replace(s,
    'log.Errorf("Failed to process history sync to database: %v", err)',
    '''log.Errorf("Failed to process history sync to database: %v", err)
        } else {
            RecordMCPEvent(ctx,client.Store.ID.ToNonAD().String(),"history.sync","","")'''))
edit("src/usecase/message.go", lambda s: replace(s,
    "GetMessageByIDChatAndDevice(request.MessageID, dataWaRecipient.String(), deviceID)",
    "GetMessageByIDChatAndDevice(deviceID, dataWaRecipient.String(), request.MessageID)"))
edit("src/usecase/send.go", lambda s: replace(s,
    '''logrus.Warnf("Failed to store sent message %s to %s: %v", ts.ID, recipient.String(), err)
			}
		}''',
    '''logrus.Warnf("Failed to store sent message %s to %s: %v", ts.ID, recipient.String(), err)
			}
        } else {
            whatsapp.RecordMCPEvent(storeCtx,deviceIDFromContext(storeCtx),"message.sent",recipient.ToNonAD().String(),ts.ID)
		}'''))
edit("src/ui/mcp/native.go", lambda s: replace(s,
    "d,_,err:=h.resolver.ResolveDevice(requested)",
    '''if h.resolver==nil{h.rpcError(w,envelope.ID,"device manager unavailable");return}
     d,_,err:=h.resolver.ResolveDevice(requested)'''))
p=Path("src/.env.example")
if "MCP_STREAMING_ENABLED=" not in p.read_text():
    p.write_text(p.read_text()+"""\n# Native HTTP gateway. Publish this port instead of 3000 when enabled.
MCP_STREAMING_ENABLED=false
MCP_STREAM_PORT=3001
# Keep outside public statics; contains private attachments and reference events.
MCP_DATA_DIR=storages/mcp
""")
print("Runtime integration completed.")
