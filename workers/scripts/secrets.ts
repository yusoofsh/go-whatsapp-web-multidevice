import { hashPassword } from "../src/util";
const password = process.env.GOWA_PASSWORD;
if (!password || password.length < 16)
  throw new Error(
    "Set GOWA_PASSWORD to a unique password of at least 16 characters. Never commit this password.",
  );
console.log("ADMIN_PASSWORD_HASH=" + (await hashPassword(password)));
console.log(
  "SESSION_ENCRYPTION_KEY=" +
    Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64"),
);
