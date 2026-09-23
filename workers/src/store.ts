import { Buffer } from "node:buffer";
import { BufferJSON, proto, type WAMessage } from "@whiskeysockets/baileys";
import { mediaKind, textContent } from "./util";
export const stringify = (value: unknown) =>
  JSON.stringify(value, BufferJSON.replacer);
export const parse = (value: string) => JSON.parse(value, BufferJSON.reviver);
export class Store {
  constructor(readonly sql: SqlStorage) {
    sql.exec(`
 CREATE TABLE IF NOT EXISTS state(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS contacts(jid TEXT PRIMARY KEY,name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS chats(jid TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',last_ts INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS messages(chat_jid TEXT NOT NULL,id TEXT NOT NULL,ts INTEGER NOT NULL,from_me INTEGER NOT NULL,sender TEXT,content TEXT NOT NULL,media_type TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(chat_jid,id));
 CREATE INDEX IF NOT EXISTS messages_by_chat_time ON messages(chat_jid,ts DESC,id);
 CREATE TABLE IF NOT EXISTS events(seq INTEGER PRIMARY KEY AUTOINCREMENT,topic TEXT NOT NULL,payload TEXT NOT NULL,ts INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS media(id TEXT PRIMARY KEY,mime TEXT NOT NULL,filename TEXT NOT NULL,size INTEGER NOT NULL,created INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS media_chunks(id TEXT NOT NULL,part INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(id,part));
 CREATE TABLE IF NOT EXISTS deliveries(seq INTEGER PRIMARY KEY,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL,status TEXT NOT NULL DEFAULT 'pending');
 CREATE TABLE IF NOT EXISTS sends(key TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,status TEXT NOT NULL,result TEXT,created INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS limits(key TEXT PRIMARY KEY,count INTEGER NOT NULL,reset_at INTEGER NOT NULL);
 `);
  }
  get(key: string): string | undefined {
    return this.sql
      .exec<{ value: string }>("SELECT value FROM state WHERE key=?", key)
      .toArray()[0]?.value;
  }
  set(key: string, value: string) {
    this.sql.exec(
      "INSERT INTO state(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
      key,
      value,
    );
  }
  delete(key: string) {
    this.sql.exec("DELETE FROM state WHERE key=?", key);
  }
  contact(jid: string, name: string) {
    this.sql.exec(
      "INSERT INTO contacts(jid,name) VALUES (?,?) ON CONFLICT(jid) DO UPDATE SET name=excluded.name",
      jid,
      name,
    );
  }
  chat(jid: string, name = "", ts = 0) {
    this.sql.exec(
      "INSERT INTO chats(jid,name,last_ts) VALUES (?,?,?) ON CONFLICT(jid) DO UPDATE SET name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE chats.name END,last_ts=MAX(chats.last_ts,excluded.last_ts)",
      jid,
      name,
      ts,
    );
  }
  message(message: WAMessage): boolean {
    const jid = message.key.remoteJid,
      id = message.key.id;
    if (!jid || !id || !message.message) return false;
    const body = stringify(message);
    if (body.length > 1024 * 1024)
      throw new Error("Message exceeds per-record storage safety limit");
    const ts = Number(message.messageTimestamp || 0),
      exists =
        this.sql
          .exec("SELECT id FROM messages WHERE chat_jid=? AND id=?", jid, id)
          .toArray().length > 0;
    this.sql.exec(
      "INSERT INTO messages(chat_jid,id,ts,from_me,sender,content,media_type,body) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(chat_jid,id) DO UPDATE SET ts=excluded.ts,content=excluded.content,media_type=excluded.media_type,body=excluded.body",
      jid,
      id,
      ts,
      message.key.fromMe ? 1 : 0,
      message.key.participant || "",
      textContent(message.message),
      mediaKind(message.message),
      body,
    );
    this.chat(jid, "", ts);
    return !exists;
  }
  raw(jid: string, id: string): WAMessage | undefined {
    const row = this.sql
      .exec<{
        body: string;
      }>("SELECT body FROM messages WHERE chat_jid=? AND id=?", jid, id)
      .toArray()[0];
    if (!row) return undefined;
    const decoded = proto.WebMessageInfo.fromObject(parse(row.body));
    if (!decoded.key?.id || !decoded.key.remoteJid)
      throw new Error("Stored message lacks its identity");
    return { ...decoded, key: decoded.key };
  }
  oldest(jid: string): WAMessage | undefined {
    const row = this.sql
      .exec<{
        body: string;
      }>("SELECT body FROM messages WHERE chat_jid=? ORDER BY ts ASC,id ASC LIMIT 1", jid)
      .toArray()[0];
    if (!row) return undefined;
    const decoded = proto.WebMessageInfo.fromObject(parse(row.body));
    if (!decoded.key?.id || !decoded.key.remoteJid)
      throw new Error("Stored message lacks its identity");
    return { ...decoded, key: decoded.key };
  }
  listMessages(
    jid: string,
    limit: number,
    offset: number,
    search = "",
    mediaOnly = false,
  ) {
    return this.sql
      .exec(
        "SELECT chat_jid,id,ts,from_me,sender,content,media_type FROM messages WHERE chat_jid=? AND (?='' OR instr(lower(content),lower(?))>0) AND (?=0 OR media_type<>'text') ORDER BY ts DESC,id DESC LIMIT ? OFFSET ?",
        jid,
        search,
        search,
        mediaOnly ? 1 : 0,
        limit,
        offset,
      )
      .toArray();
  }
  appendEvent(topic: string, payload: unknown, webhook: boolean) {
    const body = stringify(payload);
    if (body.length > 262144) throw new Error("Event too large");
    const row = this.sql
      .exec<{
        seq: number;
      }>("INSERT INTO events(topic,payload,ts) VALUES (?,?,?) RETURNING seq", topic, body, Date.now())
      .one();
    if (webhook)
      this.sql.exec(
        "INSERT INTO deliveries(seq,next_at) VALUES (?,?)",
        row.seq,
        Date.now(),
      );
    return row.seq;
  }
  events(after: number, limit: number) {
    const earliest = this.sql
      .exec<{ seq: number | null }>("SELECT MIN(seq) AS seq FROM events")
      .one().seq;
    const rows = this.sql
      .exec<{
        seq: number;
        topic: string;
        payload: string;
        ts: number;
      }>("SELECT seq,topic,payload,ts FROM events WHERE seq>? ORDER BY seq LIMIT ?", after, limit)
      .toArray();
    return {
      events: rows.map((r) => ({ ...r, payload: parse(r.payload) })),
      next_cursor: rows.at(-1)?.seq ?? after,
      cursor_expired: after > 0 && earliest !== null && after < earliest - 1,
    };
  }
  saveMedia(id: string, bytes: Buffer, mime: string, filename: string) {
    this.sql.exec(
      "INSERT INTO media(id,mime,filename,size,created) VALUES (?,?,?,?,?)",
      id,
      mime,
      filename,
      bytes.length,
      Date.now(),
    );
    for (
      let offset = 0, part = 0;
      offset < bytes.length;
      offset += 262144, part++
    )
      this.sql.exec(
        "INSERT INTO media_chunks(id,part,data) VALUES (?,?,?)",
        id,
        part,
        bytes.subarray(offset, offset + 262144),
      );
  }
  media(id: string) {
    const meta = this.sql
      .exec<{
        id: string;
        mime: string;
        filename: string;
        size: number;
      }>("SELECT id,mime,filename,size FROM media WHERE id=?", id)
      .toArray()[0];
    if (!meta) throw new Error("Media not found");
    const chunks = this.sql
      .exec<{
        data: ArrayBuffer;
      }>("SELECT data FROM media_chunks WHERE id=? ORDER BY part", id)
      .toArray();
    return {
      ...meta,
      bytes: Buffer.concat(chunks.map((r) => Buffer.from(r.data))),
    };
  }
  purgeEvents(days: number) {
    const cutoff = Date.now() - days * 86400000;
    this.sql.exec(
      "DELETE FROM deliveries WHERE seq IN (SELECT seq FROM events WHERE ts<? LIMIT 200)",
      cutoff,
    );
    this.sql.exec(
      "DELETE FROM events WHERE seq IN (SELECT seq FROM events WHERE ts<? LIMIT 200)",
      cutoff,
    );
  }
}
