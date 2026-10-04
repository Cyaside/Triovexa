import pg from "pg";
import { PostgresSaver } from "@langchain/langgraph-checkpoint-postgres";
import { RuntimeFailure } from "../bridge/schema.js";
import { checkpointIdentifier } from "./config.js";

export type CheckpointInitialization = {
  adminDSN: string;
  schema: string;
  role: string;
  password: string;
  /** Only for a dedicated database: PostgreSQL otherwise grants TEMPORARY via PUBLIC. */
  hardenDedicatedDatabase?: boolean;
};

/** Explicit administrative bootstrap. Runtime connections never run framework migrations. */
export async function initializeCheckpoint(options: CheckpointInitialization): Promise<void> {
  const schema = checkpointIdentifier(options.schema);
  const role = checkpointIdentifier(options.role);
  if (!options.password || options.password.includes("\0")) throw new RuntimeFailure("CHECKPOINT_INIT_INVALID");
  const saver = PostgresSaver.fromConnString(options.adminDSN, { schema: options.schema });
  try { await saver.setup(); } catch { throw new RuntimeFailure("CHECKPOINT_INIT_FAILED"); }
  finally { await saver.end(); }
  const client = new pg.Client({ connectionString: options.adminDSN });
  try {
    await client.connect();
    await client.query("BEGIN");
    const existing = await client.query("SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls FROM pg_roles WHERE rolname = $1", [options.role]);
    const prior = existing.rows[0];
    if (prior && (prior.rolsuper || prior.rolcreatedb || prior.rolcreaterole || prior.rolreplication || prior.rolbypassrls)) throw new RuntimeFailure("CHECKPOINT_ROLE_UNSAFE");
    if (prior) {
      const membership = await client.query("SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member WHERE r.rolname = $1", [options.role]);
      if (membership.rowCount) throw new RuntimeFailure("CHECKPOINT_ROLE_UNSAFE");
    }
    // Utility statements do not support bind parameters for identifiers/passwords.
    // The identifier whitelist and pg's literal encoder protect the DDL boundary.
    const attributes = `LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD ${pg.escapeLiteral(options.password)}`;
    await client.query(`${prior ? "ALTER" : "CREATE"} ROLE ${role} WITH ${attributes}`);
    await client.query(`REVOKE ALL ON SCHEMA ${schema} FROM ${role}`);
    await client.query(`REVOKE ALL ON ALL TABLES IN SCHEMA ${schema} FROM ${role}`);
    await client.query(`GRANT USAGE ON SCHEMA ${schema} TO ${role}`);
    await client.query(`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE ${schema}.checkpoints, ${schema}.checkpoint_blobs, ${schema}.checkpoint_writes TO ${role}`);
    await client.query(`GRANT SELECT ON TABLE ${schema}.checkpoint_migrations TO ${role}`);
    if (options.hardenDedicatedDatabase) {
      const result = await client.query("SELECT current_database() AS name");
      await client.query(`REVOKE TEMPORARY ON DATABASE ${pg.escapeIdentifier(String(result.rows[0].name))} FROM PUBLIC`);
    }
    await client.query("COMMIT");
  } catch (error) {
    await client.query("ROLLBACK").catch(() => undefined);
    if (error instanceof RuntimeFailure) throw error;
    throw new RuntimeFailure("CHECKPOINT_INIT_FAILED");
  } finally { await client.end(); }
}
