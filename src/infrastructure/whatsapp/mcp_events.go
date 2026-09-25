package whatsapp

import (
 "context"
 "sync/atomic"
 "time"

 "github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
 "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
 "github.com/sirupsen/logrus"
 "go.mau.fi/whatsmeow/proto/waE2E"
 "go.mau.fi/whatsmeow/types/events"
)

var mcpEventStore atomic.Pointer[mcpstore.Store]
func SetMCPEventStore(s *mcpstore.Store){mcpEventStore.Store(s)}

// RecordMCPEvent commits bounded references before notifying subscribers.
// Message text, raw event structures and encryption material never enter the journal.
func RecordMCPEvent(ctx context.Context,device,kind,chat,id string){
 s:=mcpEventStore.Load();if s==nil || device==""{return}
 bounded,cancel:=context.WithTimeout(context.WithoutCancel(ctx),5*time.Second);defer cancel()
 if _,err:=s.Append(bounded,mcpstore.Event{DeviceID:device,Type:kind,ChatJID:chat,MessageID:id});err!=nil{logrus.Errorf("MCP event journal write failed: %v",err)}
}
func recordMCPProtocolEvent(ctx context.Context,device string,raw any){
 switch e:=raw.(type){
 case *events.Message:
  if e==nil{return};kind:="message";id:=e.Info.ID
  if e.Message!=nil{
   m:=utils.UnwrapMessage(e.Message)
   if p:=m.GetProtocolMessage();p!=nil{
    switch p.GetType(){case waE2E.ProtocolMessage_REVOKE:kind="message.revoked";id=p.GetKey().GetID();case waE2E.ProtocolMessage_MESSAGE_EDIT:kind="message.edited";id=p.GetKey().GetID()}
   }
   if reaction:=m.GetReactionMessage();reaction!=nil{kind="message.reaction";id=reaction.GetKey().GetID()}
  }
  RecordMCPEvent(ctx,device,kind,e.Info.Chat.ToNonAD().String(),id)
 case *events.Receipt:
  if e.Sender.Device!=0{return}
  for _,id:=range e.MessageIDs{RecordMCPEvent(ctx,device,"message.receipt",e.Chat.ToNonAD().String(),id)}
 case *events.DeleteForMe:
  RecordMCPEvent(ctx,device,"message.deleted","",e.MessageID)
 case *events.Connected:
  RecordMCPEvent(ctx,device,"connection.connected","","")
 case *events.Disconnected:
  RecordMCPEvent(ctx,device,"connection.disconnected","","")
 case *events.LoggedOut:
  RecordMCPEvent(ctx,device,"connection.logged_out","","")
 case *events.GroupInfo:
  RecordMCPEvent(ctx,device,"group.updated",e.JID.ToNonAD().String(),"")
 }
}
