import { PostgresSaver } from "@langchain/langgraph-checkpoint-postgres";
import { MemorySaver } from "@langchain/langgraph";
import type { RunnableConfig } from "@langchain/core/runnables";
import pg from "pg";
import { RuntimeFailure, type Start } from "../bridge/schema.js";
import { checkpointIdentifier, CHECKPOINT_SCHEMA_VERSIONS } from "./config.js";

export function assertThread(config: RunnableConfig, thread: string): void {
  if (config.configurable?.thread_id !== thread) throw new RuntimeFailure("CHECKPOINT_IDENTITY_MISMATCH");
}

class ScopedPostgresSaver extends PostgresSaver {
  private checked: Promise<void> | undefined;
  constructor(private readonly scopedPool: pg.Pool, private readonly schema: string, private readonly thread: string) {
    super(scopedPool, undefined, { schema });
  }

  private privileges(): Promise<void> {
    return this.checked ??= (async () => {
      const result = await this.scopedPool.query(`SELECT r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls AS elevated,
        has_database_privilege(current_user, current_database(), 'CREATE') AS db_create,
        has_database_privilege(current_user, current_database(), 'TEMPORARY') AS db_temp,
        has_schema_privilege(current_user, $1, 'CREATE') AS schema_create,
        has_schema_privilege(current_user, $1, 'USAGE') AS schema_usage,
        EXISTS(SELECT 1 FROM pg_auth_members WHERE member = r.oid) AS memberships
        FROM pg_roles r WHERE r.rolname = current_user`, [this.schema]);
      const row = result.rows[0];
      if (!row || row.elevated || row.db_create || row.db_temp || row.schema_create || row.memberships || !row.schema_usage) throw new RuntimeFailure("CHECKPOINT_ROLE_UNSAFE");
      const external = await this.scopedPool.query(`SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname <> $1 AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'
        AND c.relkind IN ('r', 'v', 'm', 'p', 'f') AND has_table_privilege(current_user, c.oid, 'SELECT, INSERT, UPDATE, DELETE') LIMIT 1`, [this.schema]);
      if (external.rowCount) throw new RuntimeFailure("CHECKPOINT_ROLE_UNSAFE");
      const create = await this.scopedPool.query(`SELECT 1 FROM pg_namespace n WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
        AND n.nspname NOT LIKE 'pg_%' AND has_schema_privilege(current_user, n.oid, 'CREATE') LIMIT 1`);
      const sequence = await this.scopedPool.query(`SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relkind = 'S' AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_%'
        AND has_sequence_privilege(current_user, c.oid, 'USAGE, SELECT, UPDATE') LIMIT 1`);
      if (create.rowCount || sequence.rowCount) throw new RuntimeFailure("CHECKPOINT_ROLE_UNSAFE");
      const migrations = await this.scopedPool.query(`SELECT v FROM ${checkpointIdentifier(this.schema)}.checkpoint_migrations ORDER BY v`);
      if (JSON.stringify(migrations.rows.map((entry) => entry.v)) !== JSON.stringify(CHECKPOINT_SCHEMA_VERSIONS)) throw new RuntimeFailure("CHECKPOINT_INCOMPATIBLE");
    })().catch((error: unknown) => { if (error instanceof RuntimeFailure) throw error; throw new RuntimeFailure("CHECKPOINT_UNAVAILABLE"); });
  }

  override async setup(): Promise<void> { throw new RuntimeFailure("CHECKPOINT_DDL_DENIED"); }
  override async getTuple(config: RunnableConfig) { assertThread(config, this.thread); await this.privileges(); return super.getTuple(config); }
  override async put(config: RunnableConfig, ...args: Parameters<PostgresSaver["put"]> extends [unknown, ...infer Rest] ? Rest : never) {
    assertThread(config, this.thread); await this.privileges(); return super.put(config, ...args);
  }
  override async putWrites(config: RunnableConfig, ...args: Parameters<PostgresSaver["putWrites"]> extends [unknown, ...infer Rest] ? Rest : never) {
    assertThread(config, this.thread); await this.privileges(); return super.putWrites(config, ...args);
  }
  override async *list(config: RunnableConfig, options?: Parameters<PostgresSaver["list"]>[1]) {
    assertThread(config, this.thread); if (options?.before) assertThread(options.before, this.thread);
    await this.privileges(); yield* super.list(config, options);
  }
  override async deleteThread(thread: string): Promise<void> {
    if (thread !== this.thread) throw new RuntimeFailure("CHECKPOINT_IDENTITY_MISMATCH");
    await this.privileges(); await super.deleteThread(thread);
  }
}

export function checkpointSaver(start: Start): MemorySaver | PostgresSaver {
  const { checkpoint_dsn: uri, checkpoint_schema: schema } = start.transport;
  if (!uri || !schema) {
    if (start.scope.profile !== "offline-fixture") throw new RuntimeFailure("CHECKPOINT_REQUIRED");
    return new MemorySaver();
  }
  checkpointIdentifier(schema);
  return new ScopedPostgresSaver(new pg.Pool({ connectionString: uri, max: 2 }), schema, start.scope.checkpoint_thread);
}
