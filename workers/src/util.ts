import { Buffer } from "node:buffer";
export const MAX_REQUEST_BYTES = 3 * 1024 * 1024;
export function boundedInteger(
  value: unknown,
  min: number,
  max: number,
): number {
  if (
    typeof value !== "number" ||
    !Number.isSafeInteger(value) ||
    value < min ||
    value > max
  )
    throw new Error(`Expected an integer from ${min} to ${max}`);
  return value;
}
export function normalizeJid(value: string): string {
  if (value.includes("@")) {
    if (!/^[0-9]+(?:-[0-9]+)?@(s\.whatsapp\.net|g\.us|lid)$/.test(value))
      throw new Error("Unsupported WhatsApp JID");
    return value;
  }
  const digits = value.replace(/[+\s()-]/g, "");
  if (!/^\d{6,16}$/.test(digits))
    throw new Error("Use a phone with country code or a supported JID");
  return `${digits}@s.whatsapp.net`;
}
export function decodeBase64(value: string, max: number): Buffer {
  if (
    !value ||
    value.length > Math.ceil(max / 3) * 4 ||
    value.length % 4 !== 0 ||
    !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(
      value,
    )
  )
    throw new Error("Invalid or oversized base64 payload");
  const bytes = Buffer.from(value, "base64");
  if (bytes.length > max) throw new Error("Media size limit exceeded");
  return bytes;
}
export function assertPublicUrl(value: string, hosts: string): URL {
  const url = new URL(value),
    allowed = hosts
      .split(",")
      .map((s) => s.trim().toLowerCase())
      .filter(Boolean);
  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    (url.port && url.port !== "443") ||
    !allowed.includes(url.hostname.toLowerCase()) ||
    url.hostname === "localhost" ||
    /^(\[|\d+\.\d+\.\d+\.\d+$)/.test(url.hostname)
  )
    throw new Error(
      "URL requires HTTPS and an explicitly allowed public hostname",
    );
  return url;
}
export function isSafeMime(mime: string): boolean {
  return /^(image\/(png|jpeg|gif|webp)|audio\/(ogg|mpeg|mp4|aac|wav|opus)|video\/(mp4|webm)|application\/(pdf|octet-stream|zip|vnd\.[a-zA-Z0-9.+-]+)|text\/plain)$/.test(
    mime,
  );
}
export function unwrap(message: any): any {
  let m = message;
  for (let i = 0; i < 5; i++) {
    const inner =
      m?.ephemeralMessage?.message ??
      m?.viewOnceMessage?.message ??
      m?.viewOnceMessageV2?.message ??
      m?.documentWithCaptionMessage?.message;
    if (!inner) break;
    m = inner;
  }
  return m;
}
export function textContent(message: any): string {
  const m = unwrap(message);
  return String(
    m?.conversation ??
      m?.extendedTextMessage?.text ??
      m?.imageMessage?.caption ??
      m?.videoMessage?.caption ??
      m?.documentMessage?.caption ??
      "",
  ).slice(0, 65536);
}
export function mediaKind(message: any): string {
  const m = unwrap(message);
  for (const type of ["image", "video", "audio", "document", "sticker"])
    if (m?.[`${type}Message`]) return type;
  return "text";
}
export function escapeHtml(value: string): string {
  return value.replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ]!,
  );
}
export function json(value: unknown, status = 200): Response {
  return Response.json(value, {
    status,
    headers: {
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
    },
  });
}
export function equal(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let difference = 0;
  for (let i = 0; i < a.length; i++)
    difference |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return difference === 0;
}
export async function readBounded(
  request: Request,
  max = MAX_REQUEST_BYTES,
): Promise<Buffer> {
  if (Number(request.headers.get("content-length") || 0) > max)
    throw new Error("Request too large");
  if (!request.body) return Buffer.alloc(0);
  const reader = request.body.getReader(),
    chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const part = await reader.read();
      if (part.done) break;
      size += part.value.byteLength;
      if (size > max) {
        await reader.cancel();
        throw new Error("Request too large");
      }
      chunks.push(part.value);
    }
  } finally {
    reader.releaseLock();
  }
  return Buffer.concat(chunks);
}
export async function sha256(value: string | Uint8Array): Promise<string> {
  return Buffer.from(
    await crypto.subtle.digest(
      "SHA-256",
      typeof value === "string" ? new TextEncoder().encode(value) : value,
    ),
  ).toString("hex");
}
export async function hashPassword(
  password: string,
  salt: Uint8Array = crypto.getRandomValues(new Uint8Array(16)),
): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(password),
    "PBKDF2",
    false,
    ["deriveBits"],
  );
  const bits = await crypto.subtle.deriveBits(
    { name: "PBKDF2", salt, iterations: 100000, hash: "SHA-256" },
    key,
    256,
  );
  return `pbkdf2:100000:${Buffer.from(salt).toString("hex")}:${Buffer.from(bits).toString("hex")}`;
}
export async function verifyPassword(
  password: string,
  encoded: string,
): Promise<boolean> {
  if (
    !/^pbkdf2:100000:[a-f0-9]{32}:[a-f0-9]{64}$/.test(encoded) ||
    password.length > 1024
  )
    return false;
  return equal(
    await hashPassword(password, Buffer.from(encoded.split(":")[2], "hex")),
    encoded,
  );
}
async function encryptionKey(secret: string) {
  const key = decodeBase64(secret, 32);
  if (key.length !== 32)
    throw new Error("SESSION_ENCRYPTION_KEY must encode 32 random bytes");
  return crypto.subtle.importKey("raw", key, "AES-GCM", false, [
    "encrypt",
    "decrypt",
  ]);
}
export async function encrypt(value: string, secret: string): Promise<string> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const bytes = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv },
    await encryptionKey(secret),
    new TextEncoder().encode(value),
  );
  return `${Buffer.from(iv).toString("base64")}.${Buffer.from(bytes).toString("base64")}`;
}
export async function decrypt(value: string, secret: string): Promise<string> {
  const [iv, data] = value.split(".");
  return new TextDecoder().decode(
    await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: Buffer.from(iv, "base64") },
      await encryptionKey(secret),
      Buffer.from(data, "base64"),
    ),
  );
}
export async function hmac(value: string, secret: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return Buffer.from(
    await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(value)),
  ).toString("hex");
}
export function cookie(request: Request, name: string): string {
  return (
    (request.headers.get("cookie") || "")
      .split(";")
      .map((s) => s.trim())
      .find((s) => s.startsWith(`${name}=`))
      ?.slice(name.length + 1) || ""
  );
}
