package mcp

import (
 "bytes"
 "context"
 "encoding/base64"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "mime"
 "mime/multipart"
 "net/http"
 "net/textproto"
 "os"
 "path/filepath"
 "strings"

 "github.com/aldinokemal/go-whatsapp-web-multidevice/config"
 domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
 "github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
 "github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
 mcpg "github.com/mark3labs/mcp-go/mcp"
 "github.com/mark3labs/mcp-go/server"
)

// Persistent attachments are owned by paired identities, not reusable aliases.
func dataDevice(ctx context.Context)(string,error){
 d,ok:=whatsapp.DeviceFromContext(ctx)
 if !ok || d==nil || d.JID()==""{return "",errors.New("select a paired device to access MCP data")}
 return d.JID(),nil
}
func registerDataTools(s *server.MCPServer,data *mcpstore.Store,resolver deviceResolver){
 tool:=mcpg.NewTool("whatsapp_media",mcpg.WithDescription("Stage an attachment with action=upload, data_base64, filename, mime_type; then send using whatsapp_send media_id. action=read returns bytes over MCP. Maximum 10 MiB/file, 24-hour expiry, device-scoped. No server paths."),mcpg.WithReadOnlyHintAnnotation(false),mcpg.WithDestructiveHintAnnotation(false),mcpg.WithRawInputSchema(json.RawMessage(`{
 "type":"object","required":["action"],"properties":{
 "action":{"type":"string","enum":["upload","read"]},"device_id":{"type":"string"},
 "data_base64":{"type":"string","maxLength":13981016},"filename":{"type":"string","minLength":1,"maxLength":160},"mime_type":{"type":"string","maxLength":120},"media_id":{"type":"string","maxLength":36},"include_data":{"type":"boolean","description":"Include data_base64 in structuredContent for clients which discard binary content; default false"}},
 "allOf":[{"if":{"properties":{"action":{"const":"upload"}}},"then":{"required":["data_base64","filename","mime_type"]}},{"if":{"properties":{"action":{"const":"read"}}},"then":{"required":["media_id"]}}]}`)))
 tool.InputSchema=mcpg.ToolInputSchema{}
 s.AddTool(tool,func(ctx context.Context,r mcpg.CallToolRequest)(*mcpg.CallToolResult,error){
  ctx,_,err:=resolveDeviceContext(ctx,r,resolver);if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
  device,err:=dataDevice(ctx);if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
  switch r.GetString("action",""){
  case "upload":
   encoded:=r.GetString("data_base64","")
   if len(encoded)>base64.StdEncoding.EncodedLen(mcpstore.MaxMediaBytes){return mcpg.NewToolResultError("attachment exceeds 10 MiB"),nil}
   raw,err:=base64.StdEncoding.Strict().DecodeString(encoded);if err!=nil{return mcpg.NewToolResultError("invalid base64 attachment"),nil}
   m,err:=data.Stage(ctx,device,r.GetString("filename",""),r.GetString("mime_type",""),raw)
   if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
   return mcpg.NewToolResultStructured(m,"Attachment staged; nothing sent to WhatsApp."),nil
  case "read":
   m,raw,err:=data.Media(ctx,device,r.GetString("media_id",""));if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
   return mediaResult(m,raw,true,r.GetBool("include_data",false)),nil
  default:return mcpg.NewToolResultError("unknown media action"),nil
  }
 })
 events:=mcpg.NewTool("whatsapp_events",mcpg.WithDescription("Read durable device-scoped events after cursor, limit 1..500. Save next_cursor. On cursor_expired rescan chats/history. References only; query messages separately. Native streaming clients subscribe to whatsapp://events."),mcpg.WithReadOnlyHintAnnotation(true),mcpg.WithRawInputSchema(json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string"},"cursor":{"type":"integer","minimum":0,"maximum":9007199254740991},"limit":{"type":"integer","minimum":1,"maximum":500}}}`)))
 events.InputSchema=mcpg.ToolInputSchema{}
 s.AddTool(events,func(ctx context.Context,r mcpg.CallToolRequest)(*mcpg.CallToolResult,error){
  ctx,_,err:=resolveDeviceContext(ctx,r,resolver);if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
  device,err:=dataDevice(ctx);if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
  page,err:=data.Events(ctx,device,int64(r.GetInt("cursor",0)),r.GetInt("limit",100));if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
  return mcpg.NewToolResultStructured(page,fmt.Sprintf("%d events; next_cursor=%d",len(page.Events),page.NextCursor)),nil
 })
 s.AddResource(mcpg.NewResource(mcpstore.EventResource,"WhatsApp events",mcpg.WithMIMEType("application/json")),func(ctx context.Context,r mcpg.ReadResourceRequest)([]mcpg.ResourceContents,error){
  device,err:=dataDevice(ctx);if err!=nil{return nil,err};page,err:=data.Events(ctx,device,0,100);if err!=nil{return nil,err}
  b,err:=json.Marshal(page);if err!=nil{return nil,err}
  return []mcpg.ResourceContents{mcpg.TextResourceContents{URI:r.Params.URI,MIMEType:"application/json",Text:string(b)}},nil
 })
 s.AddResourceTemplate(mcpg.NewResourceTemplate("whatsapp://media/{media_id}","Private attachment"),func(ctx context.Context,r mcpg.ReadResourceRequest)([]mcpg.ResourceContents,error){
  device,err:=dataDevice(ctx);if err!=nil{return nil,err}
  m,raw,err:=data.Media(ctx,device,strings.TrimPrefix(r.Params.URI,"whatsapp://media/"));if err!=nil{return nil,err}
  return []mcpg.ResourceContents{mediaBlob(m,raw)},nil
 })
}
func mediaBlob(m mcpstore.Media,raw []byte)mcpg.BlobResourceContents{
 return mcpg.BlobResourceContents{URI:m.URI,MIMEType:m.MIME,Blob:base64.StdEncoding.EncodeToString(raw)}
}
func mediaResult(m mcpstore.Media,raw []byte,inline,structuredBytes bool)*mcpg.CallToolResult{
 var meta any=m
 if structuredBytes{meta=struct{mcpstore.Media;Data string `json:"data_base64"`}{m,base64.StdEncoding.EncodeToString(raw)}}
 out:=mcpg.NewToolResultStructured(meta,"Attachment available through its authenticated MCP resource.")
 if inline{
  if strings.HasPrefix(m.MIME,"image/"){out.Content=append(out.Content,mcpg.ImageContent{Type:"image",MIMEType:m.MIME,Data:base64.StdEncoding.EncodeToString(raw)})
  }else{out.Content=append(out.Content,mcpg.EmbeddedResource{Type:"resource",Resource:mediaBlob(m,raw)})}
 }
 return out
}
func(h *MessageHandler)importDownload(ctx context.Context,r mcpg.CallToolRequest,resp domainMessage.DownloadMediaResponse)(*mcpg.CallToolResult,error){
 device,err:=dataDevice(ctx);if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
 dir:=filepath.Join(config.McpDataDir,"downloads");rel,err:=filepath.Rel(dir,resp.FilePath)
 if err!=nil{return mcpg.NewToolResultError("invalid download location"),nil}
 root,err:=os.OpenRoot(dir);if err!=nil{return mcpg.NewToolResultError("cannot access private download"),nil};defer root.Close()
 f,err:=root.Open(rel);if err!=nil{return mcpg.NewToolResultError("download outside private directory"),nil};defer f.Close();defer root.Remove(rel)
 info,err:=f.Stat();if err!=nil || !info.Mode().IsRegular() || info.Size()>mcpstore.MaxMediaBytes{return mcpg.NewToolResultError("invalid or oversized download"),nil}
 raw,err:=io.ReadAll(io.LimitReader(f,mcpstore.MaxMediaBytes+1));if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
 mt:=mime.TypeByExtension(filepath.Ext(resp.Filename));if mt==""{mt=http.DetectContentType(raw)}
 m,err:=h.data.Stage(ctx,device,resp.Filename,mt,raw);if err!=nil{return mcpg.NewToolResultError(err.Error()),nil}
 return mediaResult(m,raw,r.GetBool("inline",true),false),nil
}
func stagedUpload(ctx context.Context,r mcpg.CallToolRequest,data *mcpstore.Store,kind string)(*multipart.FileHeader,func(),error){
 noop:=func(){};id:=r.GetString("media_id","");if id==""{return nil,noop,nil}
 field:=map[string]string{"image":"image_url","video":"video_url","audio":"audio_url","document":"file_url","sticker":"sticker_url"}[kind]
 if field=="" || r.GetString(field,"")!=""{return nil,noop,errors.New("use exactly one of media_id or the matching URL")}
 if data==nil{return nil,noop,errors.New("attachment storage unavailable")}
 device,err:=dataDevice(ctx);if err!=nil{return nil,noop,err};m,raw,err:=data.Media(ctx,device,id);if err!=nil{return nil,noop,err}
 prefix:=map[string]string{"image":"image/","video":"video/","audio":"audio/","sticker":"image/"}[kind]
 if prefix!="" && !strings.HasPrefix(m.MIME,prefix){return nil,noop,errors.New("attachment MIME does not match send type")}
 var b bytes.Buffer;w:=multipart.NewWriter(&b);header:=make(textproto.MIMEHeader)
 header.Set("Content-Disposition",mime.FormatMediaType("form-data",map[string]string{"name":"file","filename":m.Filename}));header.Set("Content-Type",m.MIME)
 part,err:=w.CreatePart(header);if err!=nil{return nil,noop,err};if _,err=part.Write(raw);err!=nil{return nil,noop,err};if err=w.Close();err!=nil{return nil,noop,err}
 form,err:=multipart.NewReader(&b,w.Boundary()).ReadForm(mcpstore.MaxMediaBytes+4096);if err!=nil{return nil,noop,err}
 return form.File["file"][0],func(){_ = form.RemoveAll()},nil
}
