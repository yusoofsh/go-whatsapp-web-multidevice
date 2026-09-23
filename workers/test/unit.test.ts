import { describe, expect, test } from "bun:test";
import {
  normalizeJid,
  decodeBase64,
  assertPublicUrl,
  textContent,
  boundedInteger,
  isSafeMime,
  encrypt,
  decrypt,
  hashPassword,
  verifyPassword,
  equal,
} from "../src/util";
describe("validated boundaries", () => {
  test("normalizes phones and accepts scoped JIDs", () => {
    expect(normalizeJid("+62 812-123-456")).toBe("62812123456@s.whatsapp.net");
    expect(normalizeJid("123-456@g.us")).toBe("123-456@g.us");
    expect(() => normalizeJid("status@broadcast")).toThrow();
    expect(() => normalizeJid("../etc/passwd")).toThrow();
  });
  test("strict bounded base64", () => {
    expect(decodeBase64("aGVsbG8=", 8).toString()).toBe("hello");
    expect(() => decodeBase64("aGVsbG8=", 3)).toThrow();
    expect(() => decodeBase64("%%%", 8)).toThrow();
  });
  test("URLs require exact HTTPS hostname allowlist", () => {
    expect(
      assertPublicUrl("https://cdn.example.com/file", "cdn.example.com")
        .hostname,
    ).toBe("cdn.example.com");
    for (const url of [
      "http://cdn.example.com/x",
      "https://127.0.0.1/x",
      "https://172.16.1.1/x",
      "https://cdn.example.com.evil.test/x",
      "https://user:pass@cdn.example.com/x",
    ])
      expect(() => assertPublicUrl(url, "cdn.example.com")).toThrow();
  });
  test("integer and MIME bounds", () => {
    expect(boundedInteger(5, 1, 10)).toBe(5);
    expect(() => boundedInteger(1.5, 1, 10)).toThrow();
    expect(() => boundedInteger(Infinity, 1, 10)).toThrow();
    expect(isSafeMime("application/pdf")).toBe(true);
    expect(isSafeMime("text/html")).toBe(false);
    expect(isSafeMime("image/svg+xml")).toBe(false);
  });
  test("captions and wrapped messages", () => {
    expect(textContent({ imageMessage: { caption: "a photo" } })).toBe(
      "a photo",
    );
    expect(
      textContent({ ephemeralMessage: { message: { conversation: "hi" } } }),
    ).toBe("hi");
  });
  test("constant-work comparison", () => {
    expect(equal("abc", "abc")).toBe(true);
    expect(equal("abc", "abd")).toBe(false);
    expect(equal("abc", "a")).toBe(false);
  });
});
describe("stored secrets", () => {
  test("password hashes verify", async () => {
    const hash = await hashPassword("a long test passphrase");
    expect(await verifyPassword("a long test passphrase", hash)).toBe(true);
    expect(await verifyPassword("incorrect", hash)).toBe(false);
    expect(await verifyPassword("anything", "malformed")).toBe(false);
  });
  test("encrypted state rejects wrong keys", async () => {
    const key = Buffer.alloc(32, 8).toString("base64"),
      cipher = await encrypt("sensitive session data", key);
    expect(cipher).not.toContain("sensitive");
    expect(await decrypt(cipher, key)).toBe("sensitive session data");
    expect(
      decrypt(cipher, Buffer.alloc(32, 9).toString("base64")),
    ).rejects.toThrow();
  });
});
