import { DurableObject } from "cloudflare:workers";
import { Buffer } from "node:buffer";
import makeWASocket, {
  initAuthCreds,
  proto,
  DisconnectReason,
  downloadMediaMessage,
  normalizeMessageContent,
  type AuthenticationState,
  type WAMessage,
  type WASocket,
} from "@whiskeysockets/baileys";
import pino from "pino";
import qrcode from "qrcode-generator";
import type { Env } from "./env";
import { Store, parse, stringify } from "./store";
import {
  assertPublicUrl,
  boundedInteger,
  decodeBase64,
  decrypt,
  encrypt,
  hmac,
  isSafeMime,
  json,
  normalizeJid,
  readBounded,
  sha256,
  verifyPassword,
} from "./util";
import { Sessions } from "./mcp";
const logger = pino({ level: "silent" });

/** One owner/account per Worker deployment. No public routes reach this object directly. */
export class WhatsAppAccount extends DurableObject<Env> {
  readonly store: Store;
  private socket?: WASocket;
  private connecting?: Promise<void>;
  private connectionState = "disconnected";
  private qr?: string;
  private lastError?: string;
  private deliveryTask?: Promise<void>;
  private generation = 0;
  private sessions: Sessions;
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.store = new Store(ctx.storage.sql);
    this.sessions = new Sessions(this);
  }
  get maxMedia() {
    return boundedInteger(
      Number(this.env.MAX_MEDIA_BYTES || 2097152),
      1024,
      8388608,
    );
  }
  status() {
    return {
      engine: "baileys",
      status: this.connectionState,
      enabled: this.store.get("enabled") === "true",
      paired: !!this.socket?.user,
      qr: this.qr ?? null,
      last_error: this.lastError ?? null,
      history_guarantee: "best-effort; WhatsApp controls available history",
    };
  }
  private async authState(): Promise<AuthenticationState> {
    const saved = this.store.get("auth:creds");
    const creds = saved
      ? parse(await decrypt(saved, this.env.SESSION_ENCRYPTION_KEY))
      : initAuthCreds();
    if (!saved)
      this.store.set(
        "auth:creds",
        await encrypt(stringify(creds), this.env.SESSION_ENCRYPTION_KEY),
      );
    return {
      creds,
      keys: {
        get: async (type, ids) => {
          const result: Record<string, any> = {};
          for (const id of ids) {
            const value = this.store.get(`auth:key:${type}:${id}`);
            if (value) {
              let data = parse(
                await decrypt(value, this.env.SESSION_ENCRYPTION_KEY),
              );
              if (type === "app-state-sync-key")
                data = proto.Message.AppStateSyncKeyData.fromObject(data);
              result[id] = data;
            }
          }
          return result;
        },
        set: async (data) => {
          const updates: Array<[string, string | null]> = [];
          for (const [type, entries] of Object.entries(data))
            for (const [id, value] of Object.entries(entries || {}))
              updates.push([
                `auth:key:${type}:${id}`,
                value == null
                  ? null
                  : await encrypt(
                      stringify(value),
                      this.env.SESSION_ENCRYPTION_KEY,
                    ),
              ]);
          this.ctx.storage.transactionSync(() => {
            for (const [key, value] of updates)
              value === null
                ? this.store.delete(key)
                : this.store.set(key, value);
          });
        },
      },
    };
  }
  async connectWhatsApp() {
    this.store.set("enabled", "true");
    if (
      this.socket &&
      ["connected", "pairing", "connecting"].includes(this.connectionState)
    )
      return this.status();
    if (!this.connecting)
      this.connecting = this.start().finally(() => {
        this.connecting = undefined;
      });
    await this.connecting;
    return this.status();
  }
  private async start() {
    this.connectionState = "connecting";
    this.lastError = undefined;
    const generation = ++this.generation;
    const auth = await this.authState();
    const sock = makeWASocket({
      auth,
      logger,
      markOnlineOnConnect: false,
      syncFullHistory: true,
      shouldSyncHistoryMessage: () => true,
      connectTimeoutMs: 20000,
      defaultQueryTimeoutMs: 20000,
      keepAliveIntervalMs: 25000,
      getMessage: async (key) =>
        key.remoteJid && key.id
          ? (this.store.raw(key.remoteJid, key.id)?.message ?? undefined)
          : undefined,
    });
    this.socket = sock;
    sock.ev.process(async (events) => {
      if (generation !== this.generation) return;
      try {
        if (events["creds.update"]) {
          Object.assign(auth.creds, events["creds.update"]);
          this.store.set(
            "auth:creds",
            await encrypt(
              stringify(auth.creds),
              this.env.SESSION_ENCRYPTION_KEY,
            ),
          );
        }
        const connection = events["connection.update"];
        if (connection) {
          if (connection.qr) {
            this.qr = connection.qr;
            this.connectionState = "pairing";
            this.event("connection", { state: "pairing" });
          }
          if (connection.connection === "open") {
            this.qr = undefined;
            this.connectionState = "connected";
            this.event("connection", { state: "connected" });
          }
          if (connection.connection === "close") {
            this.socket = undefined;
            this.qr = undefined;
            const code = (connection.lastDisconnect?.error as any)?.output
              ?.statusCode;
            this.connectionState = "disconnected";
            this.lastError = `WhatsApp connection closed (${code ?? "unknown"})`;
            if (code === DisconnectReason.loggedOut) {
              this.store.set("enabled", "false");
              this.connectionState = "logged_out";
            }
            this.event("connection", {
              state: this.connectionState,
              code: code ?? null,
            });
            if (this.store.get("enabled") === "true")
              await this.ctx.storage.setAlarm(Date.now() + 15000);
          }
        }
        const history = events["messaging-history.set"];
        if (history) {
          this.ctx.storage.transactionSync(() => {
            for (const c of history.contacts)
              this.store.contact(
                c.id,
                c.name ?? c.notify ?? c.verifiedName ?? "",
              );
            for (const c of history.chats)
              if (c.id)
                this.store.chat(
                  c.id,
                  c.name ?? "",
                  Number(c.conversationTimestamp ?? 0),
                );
            for (const m of history.messages) this.store.message(m);
          });
          this.event("history.sync", {
            received: history.messages.length,
            is_latest: history.isLatest ?? false,
            progress: history.progress ?? null,
          });
        }
        for (const name of ["contacts.upsert", "contacts.update"] as const)
          for (const c of events[name] ?? [])
            if (c.id)
              this.store.contact(
                c.id,
                c.name ?? c.notify ?? c.verifiedName ?? "",
              );
        for (const name of ["chats.upsert", "chats.update"] as const)
          for (const c of events[name] ?? [])
            if (c.id)
              this.store.chat(
                c.id,
                c.name ?? "",
                Number(c.conversationTimestamp ?? 0),
              );
        for (const m of events["messages.upsert"]?.messages ?? [])
          if (this.store.message(m))
            this.event("message", {
              chat_jid: m.key.remoteJid,
              message_id: m.key.id,
              from_me: !!m.key.fromMe,
              timestamp: Number(m.messageTimestamp ?? 0),
            });
        for (const { key, update } of events["messages.update"] ?? [])
          if (key.remoteJid && key.id) {
            const old = this.store.raw(key.remoteJid, key.id);
            if (old)
              this.store.message({ ...old, ...update, key } as WAMessage);
            this.event("message.update", {
              chat_jid: key.remoteJid,
              message_id: key.id,
              status: update.status ?? null,
            });
          }
        for (const reaction of events["messages.reaction"] ?? [])
          this.event("message.reaction", reaction);
      } catch (error) {
        this.lastError =
          "WhatsApp event persistence failed; check storage and quotas before continuing";
        console.error(
          "WhatsApp event persistence failed",
          error instanceof Error ? error.name : "Error",
        );
      }
    });
    await this.ctx.storage.setAlarm(Date.now() + 60000);
  }
  async disconnect(logout = false) {
    this.store.set("enabled", "false");
    ++this.generation;
    const sock = this.socket;
    this.socket = undefined;
    this.qr = undefined;
    this.connectionState = "disconnected";
    if (sock) {
      if (logout) await sock.logout();
      else sock.end(new Error("Disconnected by owner"));
    }
    if (logout) {
      this.store.sql.exec("DELETE FROM state WHERE key LIKE 'auth:%'");
      this.connectionState = "logged_out";
    }
    this.event("connection", { state: this.connectionState });
    return this.status();
  }
  private online(): WASocket {
    if (!this.socket || this.connectionState !== "connected")
      throw new Error(
        "WhatsApp is not connected. Pair it in the owner dashboard first.",
      );
    return this.socket;
  }
  private destination(value: string) {
    const jid = normalizeJid(value),
      allowed = (this.env.WHATSAPP_ALLOWED_JIDS || "")
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean)
        .map(normalizeJid);
    if (allowed.length && !allowed.includes(jid))
      throw new Error("Recipient is outside WHATSAPP_ALLOWED_JIDS");
    return jid;
  }
  async history(chat: string, count: number) {
    const jid = normalizeJid(chat),
      oldest = this.store.oldest(jid);
    if (!oldest)
      throw new Error("No stored anchor; wait for initial history sync");
    const id = await this.online().fetchMessageHistory(
      boundedInteger(count, 1, 100),
      oldest.key,
      oldest.messageTimestamp ?? 0,
    );
    return {
      status: "requested",
      request_id: id,
      chat_jid: jid,
      note: "Asynchronous best-effort request; inspect events and query messages again.",
    };
  }
  private async mediaInput(
    args: Record<string, any>,
  ): Promise<{ bytes: Buffer; mime: string; filename: string }> {
    if (
      [args.media_id, args.data_base64, args.url].filter(Boolean).length !== 1
    )
      throw new Error("Provide exactly one of media_id, data_base64, or url");
    if (args.media_id) {
      const media = this.store.media(args.media_id);
      if (media.size > this.maxMedia)
        throw new Error("Media exceeds configured limit");
      return media;
    }
    let bytes: Buffer;
    if (args.data_base64) bytes = decodeBase64(args.data_base64, this.maxMedia);
    else {
      const url = assertPublicUrl(args.url, this.env.MEDIA_ALLOWED_HOSTS || "");
      const response = await fetch(url.toString(), {
        redirect: "manual",
        signal: AbortSignal.timeout(15000),
      });
      if (!response.ok) {
        await response.body?.cancel();
        throw new Error(
          `Media host returned ${response.status}; redirects are disabled`,
        );
      }
      bytes = await readBounded(
        new Request(url.toString(), {
          method: "POST",
          headers: response.headers,
          body: response.body,
        }),
        this.maxMedia,
      );
    }
    const mime = args.mime_type || "application/octet-stream";
    if (!isSafeMime(mime)) throw new Error("Unsupported MIME type");
    return {
      bytes,
      mime,
      filename: String(args.filename || "attachment")
        .replace(/[^a-zA-Z0-9._-]/g, "_")
        .slice(0, 160),
    };
  }
  async stageMedia(args: Record<string, any>) {
    const media = await this.mediaInput(args),
      id = crypto.randomUUID();
    this.ctx.storage.transactionSync(() =>
      this.store.saveMedia(id, media.bytes, media.mime, media.filename),
    );
    return {
      media_id: id,
      uri: `whatsapp://media/${id}`,
      mime_type: media.mime,
      filename: media.filename,
      size: media.bytes.length,
    };
  }
  async send(args: Record<string, any>) {
    const key = String(args.idempotency_key || "");
    if (key.length < 8 || key.length > 128)
      throw new Error("Provide an idempotency key of 8 to 128 characters");
    const fingerprint = await sha256(JSON.stringify(args));
    const prior = this.store.sql
      .exec<{
        fingerprint: string;
        status: string;
        result: string | null;
      }>("SELECT fingerprint,status,result FROM sends WHERE key=?", key)
      .toArray()[0];
    if (prior) {
      if (prior.fingerprint !== fingerprint)
        throw new Error("Idempotency key already used for different arguments");
      if (prior.status === "completed" && prior.result)
        return JSON.parse(prior.result);
      throw new Error(
        "Send pending or outcome unknown. Inspect history; do not blindly retry using a new key.",
      );
    }
    this.online();
    this.destination(args.phone);
    this.store.sql.exec(
      "INSERT INTO sends(key,fingerprint,status,created) VALUES (?,?,?,?)",
      key,
      fingerprint,
      "pending",
      Date.now(),
    );
    try {
      const value = await this.sendOnce(args);
      this.store.sql.exec(
        "UPDATE sends SET status=?,result=? WHERE key=?",
        "completed",
        JSON.stringify(value),
        key,
      );
      return value;
    } catch (error) {
      this.store.sql.exec(
        "UPDATE sends SET status=? WHERE key=?",
        "unknown",
        key,
      );
      throw error;
    }
  }
  private async sendOnce(args: Record<string, any>) {
    const sock = this.online(),
      jid = this.destination(args.phone);
    let content: any;
    switch (args.type) {
      case "text":
        if (!args.message) throw new Error("message is required");
        content = { text: args.message };
        break;
      case "image":
      case "video":
      case "audio":
      case "document":
      case "sticker": {
        const media = await this.mediaInput(args);
        content = { [args.type]: media.bytes, mimetype: media.mime };
        if (args.type === "document") content.fileName = media.filename;
        if (args.caption) content.caption = args.caption;
        // Workers cannot execute native sharp/FFmpeg: callers supply already encoded media.
        if (["image", "video", "document"].includes(args.type))
          content.jpegThumbnail = Buffer.alloc(0);
        if (args.type === "audio") content.ptt = !!args.ptt;
        break;
      }
      case "location":
        content = {
          location: {
            degreesLatitude: args.latitude,
            degreesLongitude: args.longitude,
          },
        };
        break;
      case "contact": {
        const phone = normalizeJid(args.contact_phone).split("@")[0],
          name = String(args.contact_name).replace(/[\r\n]/g, " ");
        content = {
          contacts: {
            displayName: name,
            contacts: [
              {
                vcard: `BEGIN:VCARD\nVERSION:3.0\nFN:${name}\nTEL;type=CELL;waid=${phone}:${phone}\nEND:VCARD`,
              },
            ],
          },
        };
        break;
      }
      case "poll":
        content = {
          poll: {
            name: args.question,
            values: args.options,
            selectableCount: args.max_answer ?? 1,
          },
        };
        break;
      case "forward": {
        const original = this.store.raw(
          normalizeJid(args.source_chat_jid),
          args.message_id,
        );
        if (!original) throw new Error("Original message is not stored");
        content = { forward: original };
        break;
      }
      default:
        throw new Error("Unsupported message type");
    }
    const quoted = args.reply_message_id
      ? this.store.raw(jid, args.reply_message_id)
      : undefined;
    if (args.reply_message_id && !quoted)
      throw new Error("Quoted message is not stored in this chat");
    const sent = await sock.sendMessage(jid, content, { quoted });
    if (!sent) throw new Error("WhatsApp returned no message ID");
    this.store.message(sent);
    this.event("message.sent", { chat_jid: jid, message_id: sent.key.id });
    return {
      message_id: sent.key.id,
      chat_jid: jid,
      status: "submitted",
      note: "Submitted, not proof of delivery. Receipts are asynchronous.",
    };
  }
  async message(args: Record<string, any>): Promise<Record<string, any>> {
    const jid = normalizeJid(args.chat_jid),
      message = this.store.raw(jid, args.message_id);
    if (!message) throw new Error("Message not found in stored history");
    if (args.action === "download_media") {
      const payload = normalizeMessageContent(message.message),
        media =
          payload?.imageMessage ??
          payload?.videoMessage ??
          payload?.audioMessage ??
          payload?.documentMessage ??
          payload?.stickerMessage;
      if (!media) throw new Error("Message has no downloadable media");
      if (Number(media.fileLength ?? 0) > this.maxMedia)
        throw new Error("Media exceeds configured limit");
      const stream = await downloadMediaMessage(
        message,
        "stream",
        {},
        { logger, reuploadRequest: this.online().updateMediaMessage },
      );
      const chunks: Buffer[] = [];
      let size = 0;
      for await (const chunk of stream) {
        size += chunk.length;
        if (size > this.maxMedia) {
          stream.destroy();
          throw new Error("Media exceeds configured limit");
        }
        chunks.push(Buffer.from(chunk));
      }
      const id = crypto.randomUUID(),
        mime = isSafeMime(media.mimetype || "")
          ? media.mimetype!
          : "application/octet-stream",
        filename = String((media as any).fileName || `attachment-${id}`)
          .replace(/[^a-zA-Z0-9._-]/g, "_")
          .slice(0, 160);
      this.ctx.storage.transactionSync(() =>
        this.store.saveMedia(id, Buffer.concat(chunks), mime, filename),
      );
      return {
        media_id: id,
        uri: `whatsapp://media/${id}`,
        mime_type: mime,
        filename,
        size,
      };
    }
    const sock = this.online();
    this.destination(jid);
    switch (args.action) {
      case "react":
        await sock.sendMessage(jid, {
          react: { key: message.key, text: args.emoji || "" },
        });
        break;
      case "edit":
        if (!message.key.fromMe)
          throw new Error("Only your own sent messages can be edited");
        await sock.sendMessage(jid, { edit: message.key, text: args.message });
        break;
      case "revoke":
        if (!message.key.fromMe)
          throw new Error("Only your own sent messages can be revoked");
        await sock.sendMessage(jid, { delete: message.key });
        break;
      case "mark_read":
        await sock.readMessages([message.key]);
        break;
      default:
        throw new Error("Unsupported action");
    }
    return {
      status: "submitted",
      action: args.action,
      message_id: args.message_id,
    };
  }
  event(topic: string, payload: unknown) {
    const seq = this.ctx.storage.transactionSync(() =>
      this.store.appendEvent(topic, payload, !!this.env.WEBHOOK_URL),
    );
    this.sessions.notify();
    if (this.env.WEBHOOK_URL) this.ctx.waitUntil(this.deliver());
    return seq;
  }
  private async deliver() {
    if (this.deliveryTask) return this.deliveryTask;
    this.deliveryTask = this.deliverBatch().finally(() => {
      this.deliveryTask = undefined;
    });
    return this.deliveryTask;
  }
  private async deliverBatch() {
    if (!this.env.WEBHOOK_URL || !this.env.WEBHOOK_SECRET) return;
    const url = assertPublicUrl(
      this.env.WEBHOOK_URL,
      new URL(this.env.WEBHOOK_URL).hostname,
    );
    const rows = this.store.sql
      .exec<{
        seq: number;
        attempts: number;
        topic: string;
        payload: string;
        ts: number;
      }>("SELECT d.seq,d.attempts,e.topic,e.payload,e.ts FROM deliveries d JOIN events e ON d.seq=e.seq WHERE d.status='pending' AND d.next_at<=? ORDER BY d.seq LIMIT 5", Date.now())
      .toArray();
    for (const row of rows) {
      const body = JSON.stringify({
        id: row.seq,
        event: row.topic,
        timestamp: row.ts,
        payload: parse(row.payload),
      });
      let success = false;
      try {
        const response = await fetch(url.toString(), {
          method: "POST",
          body,
          redirect: "manual",
          signal: AbortSignal.timeout(10000),
          headers: {
            "content-type": "application/json",
            "x-gowa-event-id": String(row.seq),
            "x-gowa-signature-256":
              "sha256=" + (await hmac(body, this.env.WEBHOOK_SECRET)),
          },
        });
        success = response.ok;
        await response.body?.cancel();
      } catch {}
      const attempts = row.attempts + 1;
      this.store.sql.exec(
        "UPDATE deliveries SET attempts=?,status=?,next_at=? WHERE seq=?",
        attempts,
        success ? "delivered" : attempts >= 6 ? "failed" : "pending",
        Date.now() + Math.min(3600000, 60000 * 2 ** attempts),
        row.seq,
      );
    }
  }
  private allow(kind: string, ip: string, limit: number, window: number) {
    const now = Date.now();
    this.store.sql.exec("DELETE FROM limits WHERE reset_at<?", now);
    const row = this.store.sql
      .exec<{
        count: number;
      }>("INSERT INTO limits(key,count,reset_at) VALUES (?,1,?) ON CONFLICT(key) DO UPDATE SET count=count+1 RETURNING count", `${kind}:${ip}`, now + window)
      .one();
    return row.count <= limit;
  }
  async alarm() {
    try {
      if (this.store.get("enabled") === "true" && !this.socket)
        await this.connectWhatsApp();
      await this.deliver();
      this.ctx.storage.transactionSync(() =>
        this.store.purgeEvents(
          boundedInteger(Number(this.env.EVENT_RETENTION_DAYS || 7), 1, 90),
        ),
      );
      this.sessions.cleanup();
      for (const row of this.store.sql
        .exec<{
          key: string;
          value: string;
        }>("SELECT key,value FROM state WHERE key LIKE 'session:%' LIMIT 200")
        .toArray())
        if (JSON.parse(row.value).expires < Date.now())
          this.store.delete(row.key);
    } catch {
      this.lastError =
        "Maintenance or reconnect failed; check health and quotas";
    } finally {
      if (this.store.get("enabled") === "true" || this.env.WEBHOOK_URL)
        await this.ctx.storage.setAlarm(Date.now() + 60000);
    }
  }
  async fetch(request: Request): Promise<Response> {
    const path = new URL(request.url).pathname;
    try {
      if (path === "/mcp") return await this.sessions.handle(request);
      if (path === "/internal/limit") {
        const data = (await request.json()) as any;
        return json({
          allowed:
            this.allow(
              `global:${data.kind}`,
              "all",
              data.kind === "register" ? 50 : 200,
              86400000,
            ) && this.allow(data.kind, data.ip, 5, 60000),
        });
      }
      if (path === "/internal/password") {
        const data = (await request.json()) as any;
        return json({
          valid: await verifyPassword(
            String(data.password || ""),
            this.env.ADMIN_PASSWORD_HASH,
          ),
        });
      }
      if (path === "/internal/session") {
        const data = (await request.json()) as any;
        if (data.create) {
          const token = Buffer.from(
            crypto.getRandomValues(new Uint8Array(32)),
          ).toString("base64url");
          this.store.set(
            "session:" + (await sha256(token)),
            JSON.stringify({ expires: Date.now() + 3600000 }),
          );
          return json({ token });
        }
        const key = "session:" + (await sha256(String(data.token || "")));
        if (data.remove) {
          this.store.delete(key);
          return json({ valid: false });
        }
        const saved = this.store.get(key);
        return json({
          valid: !!saved && JSON.parse(saved).expires > Date.now(),
        });
      }
      if (path === "/api/status" && request.method === "GET") {
        let svg: string | null = null;
        if (this.qr) {
          const code = qrcode(0, "L");
          code.addData(this.qr);
          code.make();
          svg = code.createSvgTag({ cellSize: 4, margin: 4, scalable: true });
        }
        return json({ ...this.status(), qr_svg: svg });
      }
      if (path === "/api/connect" && request.method === "POST")
        return json(await this.connectWhatsApp());
      if (path === "/api/disconnect" && request.method === "POST")
        return json(await this.disconnect());
      if (path === "/api/logout" && request.method === "POST")
        return json(await this.disconnect(true));
      if (path === "/api/events" && request.method === "GET")
        return json(
          this.store.events(
            boundedInteger(
              Number(new URL(request.url).searchParams.get("after") || 0),
              0,
              Number.MAX_SAFE_INTEGER,
            ),
            100,
          ),
        );
      return json({ error: "Not found" }, 404);
    } catch (error) {
      return json(
        { error: error instanceof Error ? error.message : "Request failed" },
        400,
      );
    }
  }
}
