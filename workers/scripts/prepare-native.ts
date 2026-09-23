import { createHash } from "node:crypto";
import { readFile, mkdir, writeFile } from "node:fs/promises";
// Adapt the pinned dependency, not the protocol: Workers must import precompiled WASM.
const source = await readFile(
  "node_modules/whatsapp-rust-bridge/dist/index.js",
  "utf8",
);
if (
  createHash("sha256").update(source).digest("hex") !==
  "13586c5d558b7a683f1aa5678dadf8cbe664182fbc63608d294c9a5c7378e206"
)
  throw new Error(
    "Rust bridge changed; review the Workers adaptation before upgrading",
  );
const match = source.match(
  /initSync\(\{ module: base64ToUint8Array\("([A-Za-z0-9+/=]+)"\) \}\)/,
);
const start = source.indexOf("var forceNoSimd ="),
  end = source.indexOf("export {", start);
if (!match || start < 0 || end < start)
  throw new Error("Unsupported Rust bridge layout");
await mkdir(".generated", { recursive: true });
await writeFile(".generated/whatsapp.wasm", Buffer.from(match[1], "base64"));
await writeFile(
  ".generated/bridge.js",
  'import module from "./whatsapp.wasm";\n' +
    source.slice(0, start) +
    "initSync({module});\nvar __wasmSimdActive = false;\n" +
    source.slice(end),
);
console.log("Prepared pinned WhatsApp WASM for Workers");
