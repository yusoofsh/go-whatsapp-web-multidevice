import { EventEmitter } from "node:events";
import { Buffer } from "node:buffer";
/** Node ws subset consumed by Baileys, with no Node HTTP/socket implementation. */
export default class WorkerWebSocket extends EventEmitter {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = 0;
  private socket?: WebSocket;
  private stopped = false;
  private abort = new AbortController();
  constructor(url: string | URL, options: any = {}) {
    super();
    queueMicrotask(() => void this.open(url, options));
  }
  private async open(input: string | URL, options: any) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      if (options.agent)
        throw new Error("Custom Node agents are unsupported on Workers");
      const url = new URL(input);
      if (url.protocol !== "wss:" || url.hostname !== "web.whatsapp.com")
        throw new Error("Unsupported WhatsApp websocket origin");
      url.protocol = "https:";
      timer = setTimeout(
        () => this.abort.abort(),
        options.handshakeTimeout ?? 20000,
      );
      const response = await fetch(url.toString(), {
        headers: {
          ...options.headers,
          Origin: options.origin ?? "https://web.whatsapp.com",
          Upgrade: "websocket",
        },
        redirect: "manual",
        signal: this.abort.signal,
      });
      clearTimeout(timer);
      const ws = response.webSocket;
      if (!ws || response.status !== 101)
        throw new Error(`WhatsApp websocket rejected (${response.status})`);
      this.socket = ws;
      ws.binaryType = "arraybuffer";
      ws.accept();
      if (this.stopped) {
        ws.close(1000);
        return;
      }
      ws.addEventListener("message", (event) =>
        this.emit(
          "message",
          Buffer.from(event.data as any),
          typeof event.data !== "string",
        ),
      );
      ws.addEventListener("error", () =>
        this.emit("error", new Error("WhatsApp websocket error")),
      );
      ws.addEventListener("close", (event) => {
        this.readyState = 3;
        this.emit("close", event.code, Buffer.from(event.reason));
      });
      this.readyState = 1;
      this.emit("open");
    } catch (error) {
      this.readyState = 3;
      if (!this.stopped) this.emit("error", error);
      this.emit("close", 1006, Buffer.from("connection failed"));
    } finally {
      if (timer) clearTimeout(timer);
    }
  }
  send(value: any, callback?: (error?: Error) => void) {
    try {
      if (this.readyState !== 1 || !this.socket)
        throw new Error("WhatsApp websocket is not open");
      this.socket.send(value);
      callback?.();
    } catch (error) {
      if (callback) callback(error as Error);
      else this.emit("error", error);
    }
  }
  close(code = 1000, reason = "") {
    this.stopped = true;
    this.abort.abort();
    if (this.socket && this.readyState === 1) {
      this.readyState = 2;
      this.socket.close(code, reason);
    } else if (this.readyState !== 3) {
      this.readyState = 3;
      this.emit("close", code, Buffer.from(reason));
    }
  }
  terminate() {
    this.close();
  }
}
export { WorkerWebSocket as WebSocket };
