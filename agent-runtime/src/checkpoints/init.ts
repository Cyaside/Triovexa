import { initializeCheckpoint } from "./initialize.js";
import { RuntimeFailure } from "../bridge/schema.js";

const adminDSN = process.env.TRIOVEXA_CHECKPOINT_ADMIN_DSN;
const schema = process.env.TRIOVEXA_CHECKPOINT_SCHEMA;
const role = process.env.TRIOVEXA_CHECKPOINT_ROLE;
const password = process.env.TRIOVEXA_CHECKPOINT_ROLE_PASSWORD;
try {
  if (!adminDSN || !schema || !role || !password) throw new RuntimeFailure("CHECKPOINT_INIT_INVALID");
  await initializeCheckpoint({ adminDSN, schema, role, password,
    hardenDedicatedDatabase: process.env.TRIOVEXA_CHECKPOINT_HARDEN_DEDICATED_DATABASE === "true" });
  process.stdout.write(JSON.stringify({ status: "initialized", schema }) + "\n");
} catch (error) {
  // Never emit the connection string, password, SQL or driver error text.
  process.stderr.write(JSON.stringify({ status: "failed", code: error instanceof RuntimeFailure ? error.code : "CHECKPOINT_INIT_FAILED" }) + "\n");
  process.exitCode = 1;
}
