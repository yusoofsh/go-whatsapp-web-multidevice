"""Corrections discovered by the first complete mock/SQLite regression run."""
from pathlib import Path

def replace(path,old,new,count=1):
 p=Path(path);s=p.read_text();assert s.count(old)==count,(path,s.count(old));p.write_text(s.replace(old,new,count))
replace('src/cmd/mcp_route_test.go','TestMcpEndpointListsFiveTools','TestMcpEndpointListsParityTools')
replace('src/cmd/mcp_route_test.go','require.Len(t, tools, 5)','require.Len(t, tools, 8)')
replace('src/cmd/mcp_route_test.go','"whatsapp_group", "whatsapp_app"}', '"whatsapp_group", "whatsapp_app", "whatsapp_history", "whatsapp_newsletter", "whatsapp_profile"}')
p=Path('src/infrastructure/chatstorage/sqlite_repository.go');s=p.read_text()
start=s.index('func (r *SQLiteRepository) getMigrations() []string {')
part=s[start:];assert part.count('return []string{')==1
part=part.replace('return []string{','return append([]string{',1)
assert part.endswith('\t}\n}\n')
part=part[:-len('\t}\n}\n')]+'\t}, reactionScopeMigration)\n}\n'
s=s[:start]+part
old='return r.DeleteReaction(reaction.MessageID, reaction.ReactorJID, reaction.DeviceID)'
assert s.count(old)==1
s=s.replace(old,'return r.deleteReactionInChat(reaction.MessageID, reaction.ChatJID, reaction.ReactorJID, reaction.DeviceID)')
old='WHERE message_id = ? AND reactor_jid = ? AND device_id = ?\n\t`, reaction.ChatJID, reaction.Emoji, reaction.IsFromMe, reaction.Timestamp, reaction.UpdatedAt,\n\t\treaction.MessageID, reaction.ReactorJID, reaction.DeviceID)'
assert s.count(old)==1
s=s.replace(old,'WHERE message_id = ? AND reactor_jid = ? AND device_id = ? AND chat_jid = ?\n\t`, reaction.ChatJID, reaction.Emoji, reaction.IsFromMe, reaction.Timestamp, reaction.UpdatedAt,\n\t\treaction.MessageID, reaction.ReactorJID, reaction.DeviceID, reaction.ChatJID)')
p.write_text(s)
p=Path('docs/capability-map.md');p.write_text(p.read_text()+'\n## Reaction identity repair\n\nThe existing reaction primary key omitted chat identity. An appended transactional migration preserves existing rows and adds chat JID to the key; reaction updates and incoming removal events now target the full chat/device identity. New collision and migration-preservation tests cover this correction. No historical overwritten reactions can be reconstructed from absent data.\n')
print('Applied concrete test failures: catalog count and cross-chat reaction storage identity.')
