"""Resolve only the seven inspected conflicts for pinned upstream 831a851.
All non-conflicting upstream changes remain intact. Full tests gate the merge.
"""
import json
import re
import subprocess
from pathlib import Path

expected={'src/cmd/mcp_oauth.go','src/cmd/mcp_route_test.go','src/cmd/rest.go','src/infrastructure/chatstorage/sqlite_repository.go','src/ui/mcp/chat.go','src/ui/mcp/schemas.go','src/ui/mcp/server.go'}
files=set(subprocess.check_output(['git','diff','--name-only','--diff-filter=U'],text=True).splitlines())
assert files==expected,('unexpected merge surface',files)
pattern=re.compile(r'^<<<<<<< HEAD\n(.*?)^=======\n(.*?)^>>>>>>> [^\n]+\n',re.M|re.S)
for path in sorted(files):
 p=Path(path);text=p.read_text();count=0
 def resolve(match):
  global count
  count+=1;ours,theirs=match.groups()
  if path=='src/cmd/mcp_oauth.go':
   assert 'runtimeMcpDeps()' in ours and 'Schedule: scheduleUsecase' in theirs
   return ours
  if path=='src/cmd/rest.go':
   if 'runtimeMcpDeps()' in ours:
    assert 'Schedule: scheduleUsecase' in theirs
    return ours
   assert 'stopMCP()' in ours and 'scheduleStop()' in theirs
   return ours+theirs
  if path=='src/cmd/mcp_route_test.go':
   assert any(term in ours for term in ['TestMcpEndpointListsParityTools','require.Len(t, tools, 8)','whatsapp_history'])
   return ours
  if path=='src/infrastructure/chatstorage/sqlite_repository.go':
   assert 'reactionScopeMigration' in ours and 'Migration 46:' in theirs and 'scheduled_sends' in theirs
   return theirs
  if path=='src/ui/mcp/chat.go':
   assert 'case "request_history"' in ours and 'NewToolResultStructuredOnly(resp)' in theirs
   old='return mcpg.NewToolResultStructured(resp, fmt.Sprintf("Retrieved %d messages from %s", len(resp.Data), chatJID)), nil'
   assert ours.count(old)==1
   return ours.replace(old,'return mcpg.NewToolResultStructuredOnly(resp), nil')
  if path=='src/ui/mcp/schemas.go':
   left=json.loads('{'+ours+'}');right=json.loads('{'+theirs+'}')
   assert 'media_id' in left and 'scheduled_at' in right
   # Upstream owns common fields/descriptions; retain only our additional field.
   right['media_id']=left['media_id']
   return json.dumps(right,indent=2,ensure_ascii=False)[2:-2]+'\n'
  if path=='src/ui/mcp/server.go':
   assert ('type Deps' not in ours)
   assert ('domainDevice.IDeviceUsecase' in ours and 'IScheduleUsecase' in theirs) or ('deps.Data' in ours and 'InitMcpSchedule' in theirs)
   return ours
  raise AssertionError(path)
 text=pattern.sub(resolve,text)
 assert count>0 and '<<<<<<<' not in text and '>>>>>>>' not in text
 p.write_text(text)
 print('Resolved inspected conflicts:',path,count,flush=True)

def replace(path,old,new,count=1):
 p=Path(path);s=p.read_text();assert s.count(old)==count,(path,old,s.count(old));p.write_text(s.replace(old,new,count))

replace('src/ui/mcp/server.go','type Deps struct {','type Deps struct {\n Schedule domainSend.IScheduleUsecase')
replace('src/ui/mcp/server.go','InitMcpSend(deps.Send, resolver, deps.Data).AddSendTools(s)','InitMcpSend(deps.Send, resolver, deps.Data).AddSendTools(s)\n InitMcpSchedule(deps.Schedule, resolver).AddScheduleTools(s)')
replace('src/ui/mcp/server.go','// NewServer registers the five upstream tools and optional media/event tools.','// NewServer preserves upstream scheduling and the consolidated native parity tools.')
replace('src/cmd/mcp_runtime.go','Call: callUsecase}', 'Call: callUsecase, Schedule: scheduleUsecase}')
replace('src/cmd/mcp_route_test.go','require.Len(t, tools, 8)','require.Len(t, tools, 9)')
replace('src/cmd/mcp_route_test.go','"whatsapp_history", "whatsapp_newsletter", "whatsapp_profile"}', '"whatsapp_history", "whatsapp_newsletter", "whatsapp_profile", "whatsapp_schedule"}')
replace('src/ui/mcp/parity_test.go','require.Len(t, tools, 10)','require.Len(t, tools, 11)')
replace('src/ui/mcp/parity_test.go','"whatsapp_history", "whatsapp_profile", "whatsapp_newsletter"}', '"whatsapp_history", "whatsapp_profile", "whatsapp_newsletter", "whatsapp_schedule"}')
replace('scripts/mcp-container-smoke.py','assert len(names) == 10','assert len(names) == 11\nassert "whatsapp_schedule" in names')

# Validate each resulting raw schema after the semantic properties merge.
schemas=Path('src/ui/mcp/schemas.go').read_text()
for name,raw in re.findall(r'const (\w+) = `(.*?)`',schemas,re.S):
 parsed=json.loads(raw)
 if name=='sendSchema':
  assert {'media_id','scheduled_at','timezone'}<=parsed['properties'].keys()
  for kind in ['image','video','audio','document','sticker']:
   condition=next(x for x in parsed['allOf'] if x.get('if',{}).get('properties',{}).get('type',{}).get('const')==kind)
   assert 'oneOf' in condition['then'],kind
 if name=='chatSchema':assert 'request_history' in parsed['properties']['action']['enum']

# Keep upstream integer migration numbers untouched. Fork migrations have a
# separate named ledger, so later upstream releases cannot reuse our version.
replace('src/infrastructure/chatstorage/sqlite_repository.go','return append([]string{','return []string{')
p=Path('src/infrastructure/chatstorage/sqlite_repository.go');s=p.read_text()
start=s.index('func (r *SQLiteRepository) InitializeSchema() error {');end=s.index('// getSchemaVersion',start)
part=s[start:end];assert part.count('\treturn nil\n')==1
part=part.replace('\treturn nil\n','\treturn r.applyForkMigrations()\n')
s=s[:start]+part+s[end:];p.write_text(s)
p=Path('src/infrastructure/chatstorage/reaction_scope.go');s=p.read_text()
s=s.replace('package chatstorage\n','package chatstorage\n\nimport "database/sql"\n')
s=s.replace('// Appended after the existing migration sequence. Rebuild atomically under\n// runMigration\'s transaction; all existing rows and timestamps are preserved.','// Named fork migration, independent of upstream numbered migrations. The\n// complete table rebuild and ledger entry share one transaction.')
s+='''
const reactionScopeMigrationID = "20260925_reaction_chat_identity"

// applyForkMigrations does not modify schema_info, whose versions belong upstream.
func (r *SQLiteRepository) applyForkMigrations() error {
 tx,err:=r.db.Begin();if err!=nil{return err};defer tx.Rollback()
 if _,err=tx.Exec("CREATE TABLE IF NOT EXISTS gowa_fork_schema_info (migration_id TEXT PRIMARY KEY, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)");err!=nil{return err}
 var applied string
 err=tx.QueryRow("SELECT migration_id FROM gowa_fork_schema_info WHERE migration_id=?",reactionScopeMigrationID).Scan(&applied)
 if err==nil{return tx.Commit()}
 if err!=sql.ErrNoRows{return err}
 if _,err=tx.Exec(reactionScopeMigration);err!=nil{return err}
 if _,err=tx.Exec("INSERT INTO gowa_fork_schema_info(migration_id) VALUES(?)",reactionScopeMigrationID);err!=nil{return err}
 return tx.Commit()
}
'''
p.write_text(s)
p=Path('src/infrastructure/chatstorage/reaction_scope_test.go');s=p.read_text()
old='require.NoError(t, r.runMigration(reactionScopeMigration, len(r.getMigrations())))'
assert s.count(old)==1
s=s.replace(old,'''_,err=r.db.Exec("DELETE FROM gowa_fork_schema_info WHERE migration_id=?",reactionScopeMigrationID);require.NoError(t,err)
 versionBefore,err:=r.getSchemaVersion();require.NoError(t,err)
 require.NoError(t,r.applyForkMigrations())
 versionAfter,err:=r.getSchemaVersion();require.NoError(t,err);require.Equal(t,versionBefore,versionAfter)''')
p.write_text(s)

# A staged file must not send an empty-but-non-nil URL to the real validators.
p=Path('src/ui/mcp/send.go');s=p.read_text()
for name in ['image','video','audio','file','sticker']:
 old='&'+name+'URL';assert s.count(old)==1,(name,s.count(old))
 s=s.replace(old,'optionalMediaURL('+name+'URL)')
s+='''
func optionalMediaURL(value string) *string {
 if value=="" {return nil}
 return &value
}
'''
assert 'scheduleOptions(request)' in s and 'Mentions:' in s and 'upload' in s
p.write_text(s)
Path('src/ui/mcp/staged_validation_test.go').write_text('''package mcp

import (
 "context"
 "testing"

 "github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
 "github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
 "github.com/stretchr/testify/require"
)

func TestStagedMediaPassesProductionValidators(t *testing.T){
 data:=parityStore(t);device:=pairedTestDevice("a","111");ctx:=whatsapp.ContextWithDevice(context.Background(),device)
 spy:=&stubSendService{};handler:=InitMcpSend(spy,&stubResolver{inst:device},data)
 cases:=[]struct{kind,mime,name string;body []byte}{
  {"image","image/png","x.png",[]byte("\\x89PNG\\r\\n\\x1a\\nfixture")},
  {"video","video/mp4","x.mp4",[]byte("fixture-video")},
  {"audio","audio/ogg","x.ogg",[]byte("OggSfixture")},
  {"document","application/pdf","x.pdf",[]byte("%PDF-1.7\\nfixture")},
  {"sticker","image/png","x.png",[]byte("\\x89PNG\\r\\n\\x1a\\nfixture")},
 }
 for _,tc:=range cases{
  media,err:=data.Stage(ctx,device.JID(),tc.name,tc.mime,tc.body);require.NoError(t,err)
  result,err:=handler.handleSend(ctx,callReq(map[string]any{"type":tc.kind,"phone":"628123456789","media_id":media.ID}));require.NoError(t,err);require.False(t,result.IsError)
  switch tc.kind{
  case "image":require.Nil(t,spy.lastImage.ImageURL);require.NoError(t,validations.ValidateSendImage(ctx,*spy.lastImage))
  case "video":require.Nil(t,spy.lastVideo.VideoURL);require.NoError(t,validations.ValidateSendVideo(ctx,*spy.lastVideo))
  case "audio":require.Nil(t,spy.lastAudio.AudioURL);require.NoError(t,validations.ValidateSendAudio(ctx,*spy.lastAudio))
  case "document":require.Nil(t,spy.lastFile.FileURL);require.NoError(t,validations.ValidateSendFile(ctx,*spy.lastFile))
  case "sticker":require.Nil(t,spy.lastSticker.StickerURL);require.NoError(t,validations.ValidateSendSticker(ctx,*spy.lastSticker))
  }
 }
}
''')

for path in ['docs/capability-map.md','docs/composio-schema-migration.md','docs/mcp-native.md']:
 p=Path(path);s=p.read_text().replace('**10 tools**','**11 tools**').replace('registers ten\ntools: those five plus media, events, history, newsletter and profile.','registers eleven\ntools: the original five plus scheduling, media, events, history, newsletter and profile.')
 s=s.replace('An appended transactional migration preserves existing rows', 'A named fork migration, separate from upstream schema version numbers, preserves existing rows')
 if path.endswith('capability-map.md'):
  s+='\n## Upstream synchronization\n\nMerged upstream `831a851e677f48e25aa671170daade586f798551` (v9.5.0 plus its subsequent whatsmeow dependency update). `whatsapp_schedule` and scheduled-send fields are retained alongside the ten parity tools. Generated schemas include the complete scheduling contract. Fork schema repairs use a named ledger and do not occupy upstream migration numbers. Staged attachments are checked against the real production validators as well as mock send dispatch. No real WhatsApp scheduling/delivery was exercised.\n'
 if path.endswith('composio-schema-migration.md'):
  s+='\nUpstream v9.5.0 also adds `whatsapp_schedule`. Register its complete generated input schema and description from `mcp-tools.json`, and retain all upstream scheduling fields on `whatsapp_send` (including `scheduled_at`, `timezone`, recurrence and bounds). The final catalog has eleven tools.\n'
 p.write_text(s)

subprocess.run(['git','add',*sorted(files),'src/cmd/mcp_runtime.go','src/ui/mcp/parity_test.go','src/ui/mcp/send.go','src/ui/mcp/staged_validation_test.go','src/infrastructure/chatstorage/reaction_scope.go','src/infrastructure/chatstorage/reaction_scope_test.go','scripts/mcp-container-smoke.py','docs'],check=True)
assert not subprocess.check_output(['git','diff','--name-only','--diff-filter=U'],text=True).strip()
print('All seven conflicts resolved with scheduling and parity retained; full tests must pass before commit.',flush=True)
