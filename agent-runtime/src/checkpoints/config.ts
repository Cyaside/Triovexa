import { RuntimeFailure } from "../bridge/schema.js";

// @langchain/langgraph-checkpoint-postgres 1.0.5 exports five migrations, v0..v4.
// Dependency upgrades must update this compatibility gate alongside restore tests.
export const CHECKPOINT_SCHEMA_VERSIONS = Object.freeze([0, 1, 2, 3, 4]);

/** Configuration names are server-controlled and cannot select PostgreSQL system schemas. */
export function checkpointIdentifier(value: string): string {
  if (!/^[a-z][a-z0-9_]{0,62}$/.test(value) || value === "public" || value.startsWith("pg_") || value === "information_schema") {
    throw new RuntimeFailure("CHECKPOINT_INCOMPATIBLE");
  }
  return `"${value}"`;
}
