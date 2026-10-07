import { createHash, randomUUID } from "node:crypto";
import pg from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { isAIMessage, type BaseMessage } from "@langchain/core/messages";
import { checkpointSaver } from "../src/checkpoints/saver.js";
import { initializeCheckpoint } from "../src/checkpoints/initialize.js";
import { RuntimeFailure, type Start } from "../src/bridge/schema.js";
import { investigate } from "../src/runtime/engine.js";
import { Artifacts } from "../src/context/artifacts.js";
import { invokeReadOnlyReviewer, type CandidateProof } from "../src/runtime/reviewer.js";
import { completion, patchArgs, startFixture, stub } from "./fixture.js";

const databaseURL = process.env.TEST_AGENT_DATABASE_URL;
const suite = databaseURL ? describe : describe.skip;

suite("Postgres checkpoint isolation and recovery", () => {
  const suffix = randomUUID().replaceAll("-", "");
  const schema = `checkpoint_${suffix}`;
  const role = `runtime_${suffix}`;
  const domainSchema = `domain_${suffix}`;
  const password = "offline-checkpoint-role-only";
  let admin: pg.Client;
  let runtimeDSN: string;

  beforeAll(async () => {
    const url = new URL(databaseURL!);
    if (!["127.0.0.1", "localhost"].includes(url.hostname) || !["/triovexa_ai_test", "/triovexa_agent_test"].includes(url.pathname)) throw new Error("Checkpoint tests require the isolated fixture database");
    admin = new pg.Client({ connectionString: databaseURL! });
    await admin.connect();
    await admin.query(`CREATE SCHEMA "${domainSchema}"`);
    await admin.query(`CREATE TABLE "${domainSchema}".provider_secrets (value text NOT NULL)`);
    await admin.query(`CREATE SEQUENCE "${domainSchema}".private_sequence`);
    await admin.query(`INSERT INTO "${domainSchema}".provider_secrets VALUES ('dummy-domain-secret')`);
    await initializeCheckpoint({ adminDSN: databaseURL!, schema, role, password, hardenDedicatedDatabase: true });
    url.username = role; url.password = password;
    runtimeDSN = url.toString();
  }, 30000);

  afterAll(async () => {
    if (!admin) return;
    // All identifiers are locally generated and confined to this fixture suite.
    await admin.query(`DROP SCHEMA IF EXISTS "${schema}" CASCADE`);
    await admin.query(`DROP SCHEMA IF EXISTS "${domainSchema}" CASCADE`);
    await admin.query(`DROP ROLE IF EXISTS "${role}"`);
    await admin.end();
  });

  async function fixture(): Promise<Start> {
    const start = await startFixture();
    start.scope.case_id = `case-${randomUUID()}`;
    start.scope.attempt_id = `attempt-${randomUUID()}`;
    start.scope.checkpoint_thread = `${start.scope.case_id}:${start.scope.attempt_id}`;
    start.transport.checkpoint_dsn = runtimeDSN;
    start.transport.checkpoint_schema = schema;
    return start;
  }
  const config = (start: Start) => ({ configurable: { thread_id: start.scope.checkpoint_thread } });

  it("runs migrations administratively and denies domain secrets and runtime DDL", async () => {
    await initializeCheckpoint({ adminDSN: databaseURL!, schema, role, password });
    const versions = await admin.query(`SELECT v FROM "${schema}".checkpoint_migrations ORDER BY v`);
    expect(versions.rows.map((row) => row.v)).toEqual([0, 1, 2, 3, 4]);
    const runtime = new pg.Client({ connectionString: runtimeDSN });
    await runtime.connect();
    try {
      for (const query of [
        `SELECT * FROM "${domainSchema}".provider_secrets`,
        `CREATE TABLE "${schema}".unapproved (id int)`,
        "CREATE SCHEMA unauthorized_runtime_schema",
        "CREATE TEMPORARY TABLE unapproved_temp (id int)",
      ]) await expect(runtime.query(query)).rejects.toMatchObject({ code: "42501" });
      expect((await runtime.query(`SELECT count(*) FROM "${schema}".checkpoints`)).rows[0].count).toBe("0");
    } finally { await runtime.end(); }
    const start = await fixture(); const saver = checkpointSaver(start);
    try {
      expect(await saver.getTuple(config(start))).toBeUndefined();
      await expect((saver as { setup(): Promise<void> }).setup()).rejects.toMatchObject({ code: "CHECKPOINT_DDL_DENIED" });
    } finally { if ("end" in saver) await saver.end(); }
  });

  it("fences all checkpoint access and deletion to the approved thread", async () => {
    const start = await fixture(); const saver = checkpointSaver(start);
    const other = { configurable: { thread_id: "other-case:other-attempt" } };
    try {
      await expect(saver.getTuple(other)).rejects.toMatchObject({ code: "CHECKPOINT_IDENTITY_MISMATCH" });
      await expect(saver.deleteThread("other-case:other-attempt")).rejects.toMatchObject({ code: "CHECKPOINT_IDENTITY_MISMATCH" });
      const list = async () => { for await (const _ of saver.list(other)) { /* consume */ } };
      await expect(list()).rejects.toMatchObject({ code: "CHECKPOINT_IDENTITY_MISMATCH" });
      const before = async () => { for await (const _ of saver.list(config(start), { before: other })) { /* consume */ } };
      await expect(before()).rejects.toMatchObject({ code: "CHECKPOINT_IDENTITY_MISMATCH" });
    } finally { if ("end" in saver) await saver.end(); }
  });

  it("rejects future checkpoint migrations and preexisting domain grants without reading domain data", async () => {
    const start = await fixture();
    await admin.query(`INSERT INTO "${schema}".checkpoint_migrations VALUES (5)`);
    let saver = checkpointSaver(start);
    try { await expect(saver.getTuple(config(start))).rejects.toMatchObject({ code: "CHECKPOINT_INCOMPATIBLE" }); }
    finally { if ("end" in saver) await saver.end(); await admin.query(`DELETE FROM "${schema}".checkpoint_migrations WHERE v = 5`); }
    await admin.query(`GRANT USAGE ON SCHEMA "${domainSchema}" TO "${role}"`);
    await admin.query(`GRANT SELECT ON "${domainSchema}".provider_secrets TO "${role}"`);
    saver = checkpointSaver(start);
    try { await expect(saver.getTuple(config(start))).rejects.toMatchObject({ code: "CHECKPOINT_ROLE_UNSAFE" }); }
    finally {
      if ("end" in saver) await saver.end();
      await admin.query(`REVOKE SELECT ON "${domainSchema}".provider_secrets FROM "${role}"`);
      await admin.query(`REVOKE USAGE ON SCHEMA "${domainSchema}" FROM "${role}"`);
    }
    for (const [grant, revoke] of [
      [`GRANT CREATE ON SCHEMA "${domainSchema}" TO "${role}"`, `REVOKE CREATE ON SCHEMA "${domainSchema}" FROM "${role}"`],
      [`GRANT USAGE ON SEQUENCE "${domainSchema}".private_sequence TO "${role}"`, `REVOKE USAGE ON SEQUENCE "${domainSchema}".private_sequence FROM "${role}"`],
    ] as const) {
      await admin.query(grant);
      saver = checkpointSaver(start);
      try { await expect(saver.getTuple(config(start))).rejects.toMatchObject({ code: "CHECKPOINT_ROLE_UNSAFE" }); }
      finally { if ("end" in saver) await saver.end(); await admin.query(revoke); }
    }
  });

  it("persists native reasoning and immutable artifacts without transport credentials; completed resume makes no request", async () => {
    const start = await fixture();
    const gateway = await stub((request, index) => ({ body: completion(String(request.body.model), index === 1
      ? [{ id: "read-large", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 500 } }]
      : [{ id: "patch-final", name: "propose_patch", args: patchArgs }], "private-offline-reasoning") }));
    start.transport.model_gateway_url = gateway.url;
    const source = Array.from({ length: 500 }, (_, index) => `// line ${index} offline source content`).join("\n");
    const caller = async (id: string, name: string) => ({ call_id: id, status: "ok" as const, value: name === "propose_patch"
      ? { terminal: true, outcome: "patch_ready", code: "PATCH_READY" }
      : { path: "internal/worker/job.go", digest: "fixed-source-digest", content: source } });
    try {
      expect(await investigate(start, caller, new AbortController().signal)).toMatchObject({ status: "completed", model_requests: 2 });
      const saver = checkpointSaver(start);
      try {
        const stored = (await saver.getTuple(config(start)))!;
        const messages = stored.checkpoint.channel_values.messages as BaseMessage[];
        const reasoning = messages.filter(isAIMessage).map((message) => message.additional_kwargs.reasoning_content);
        expect(reasoning).toContain("private-offline-reasoning");
        const files = stored.checkpoint.channel_values.files;
        const artifacts = new Artifacts(start.scope.attempt_id);
        artifacts.restore(files);
        const entries = Object.entries(artifacts.snapshot());
        expect(entries.length).toBeGreaterThan(0);
        expect(entries.some(([, file]) => typeof file.content === "string" && file.content.includes(source))).toBe(true);
        const serialized = JSON.stringify(stored);
        expect(serialized).not.toContain(start.transport.capability);
        expect(serialized).not.toContain(runtimeDSN);
        expect(serialized).not.toContain("checkpoint_dsn");
      } finally { if ("end" in saver) await saver.end(); }
      const rotated = structuredClone(start);
      rotated.transport.capability = "rotated-ephemeral-private-capability";
      rotated.scope.deadline = new Date(Date.now() + 150000).toISOString();
      rotated.scope.baseline.duration_ns += 10;
      expect(await investigate(rotated, caller, new AbortController().signal)).toMatchObject({ status: "completed", model_requests: 2 });
      expect(gateway.requests).toHaveLength(2);
    } finally { await gateway.close(); }
  }, 30000);

  it("resumes a pending tool with its original ID without another model inference", async () => {
    const start = await fixture();
    const gateway = await stub((request, index) => ({ body: completion(String(request.body.model), index === 1
      ? [{ id: "read-crash", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 2 } }]
      : [{ id: "patch-crash", name: "propose_patch", args: patchArgs }]) }));
    start.transport.model_gateway_url = gateway.url;
    const ids: string[] = [];
    try {
      const interrupted = await investigate(start, async (id, name) => {
        ids.push(id);
        if (name === "propose_patch") throw new RuntimeFailure("OFFLINE_PROCESS_CRASH");
        return { call_id: id, status: "ok", value: { content: "package worker" } };
      }, new AbortController().signal);
      expect(interrupted.status).toBe("failed");
      expect(gateway.requests).toHaveLength(2);
      const resumed = await investigate(start, async (id) => {
        ids.push(id);
        return { call_id: id, status: "ok", value: { terminal: true, outcome: "patch_ready", code: "PATCH_READY" } };
      }, new AbortController().signal);
      expect(resumed).toMatchObject({ status: "completed", model_requests: 2 });
      expect(ids).toEqual(["read-crash", "patch-crash", "patch-crash"]);
      expect(gateway.requests).toHaveLength(2);
    } finally { await gateway.close(); }
  }, 30000);

  it("blocks altered immutable metadata or baseline instead of starting a new run", async () => {
    const start = await fixture();
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ id: "blocked-final", name: "cannot_determine", args: { reason: "offline evidence inconclusive" } }]) }));
    start.transport.model_gateway_url = gateway.url;
    const caller = async (id: string) => ({ call_id: id, status: "ok" as const, value: { terminal: true, outcome: "blocked", code: "INSUFFICIENT_EVIDENCE" } });
    try {
      expect((await investigate(start, caller, new AbortController().signal)).code).toBe("INSUFFICIENT_EVIDENCE");
      for (const changed of [
        { ...start, scope: { ...start.scope, provider_config_version: "changed-config" } },
        { ...start, scope: { ...start.scope, baseline: { ...start.scope.baseline, output: "changed baseline proof" } } },
        { ...start, transport: { ...start.transport, model: "changed-model" } },
        { ...start, transport: { ...start.transport, input_budget_mode: "gateway-preview" as const } },
      ]) expect(await investigate(changed, caller, new AbortController().signal)).toMatchObject({ status: "blocked", code: "CHECKPOINT_INCOMPATIBLE", model_requests: 0 });
      expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  }, 30000);

  it("blocks unfinished pre-archive context policy checkpoints before another model or tool call", async () => {
    const start = await fixture();
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ id: "pre-policy-read", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 2 } }]) }));
    start.transport.model_gateway_url = gateway.url;
    try {
      expect(await investigate(start, async () => { throw new RuntimeFailure("OFFLINE_PROCESS_CRASH"); }, new AbortController().signal)).toMatchObject({ status: "failed", code: "OFFLINE_PROCESS_CRASH" });
      const saver = checkpointSaver(start);
      try {
        const stored = (await saver.getTuple(config(start)))!;
        const { scope } = start;
        // Exact pre-archives identity layout, deliberately lacking context_policy.
        const oldIdentity = createHash("sha256").update(JSON.stringify({ engine: scope.engine_id, engine_version: scope.engine_version, thread: scope.checkpoint_thread, model: start.transport.model,
          config: scope.provider_config_version, prompt: scope.prompt_version, playbooks: scope.playbook_manifest_digest, base: scope.base_sha, deployed: scope.deployed_sha,
          paths: scope.allowed_paths, recipes: scope.recipe_ids, profile: scope.profile, limits: scope.limits, evidence: scope.evidence,
          baseline: { exit_code: scope.baseline.exit_code, output: scope.baseline.output } })).digest("hex");
        const version = `${stored.checkpoint.channel_versions.runtime_identity}-pre-archive-policy`;
        await saver.put(stored.config, { ...stored.checkpoint, id: `ffffffff-ffff-4fff-bfff-${randomUUID().slice(-12)}`,
          channel_values: { ...stored.checkpoint.channel_values, runtime_identity: oldIdentity }, channel_versions: { ...stored.checkpoint.channel_versions, runtime_identity: version } },
        stored.metadata!, { runtime_identity: version });
      } finally { if ("end" in saver) await saver.end(); }
      let tools = 0;
      expect(await investigate(start, async () => { tools++; throw new Error("Old context policy cannot resume"); }, new AbortController().signal)).toMatchObject({ status: "blocked", code: "CHECKPOINT_INCOMPATIBLE", model_requests: 0, tool_steps: 0 });
      expect(tools).toBe(0); expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  }, 30000);

  it("retrieves offloaded trusted instructions with a new bounded range before source and terminal patch in four requests", async () => {
    const start = await fixture(); start.scope.profile = "final-smoke"; start.transport.input_budget_mode = "gateway-preview";
    const books = ["go-investigation", "incident-evidence", "regression-patch"];
    const wires: Record<string, unknown>[] = []; let previews = 0; let effects = 0;
    const fetcher: typeof fetch = async (_input, init) => {
      const body = JSON.parse(String(init?.body)); const headers = new Headers(init?.headers);
      if (headers.get("X-Triovexa-Context-Preview") === "1") {
        previews++;
        // Local deterministic authority fixture, including template overhead;
        // the Go/Node integration proves the actual GLM template/campaign cap.
        return new Response(JSON.stringify({ input_bound: Buffer.byteLength(JSON.stringify(body)) + 1400, max_input_tokens: 6000 }), { status: 200 });
      }
      wires.push(body);
      const index = wires.length;
      if (index === 2) {
        const output = JSON.stringify(body.messages);
        expect(output).toContain("/skills/go-investigation/SKILL.md#L8");
      }
      return new Response(JSON.stringify(completion(start.transport.model, index === 1
        ? books.map((name, bookIndex) => ({ id: `book-${bookIndex}`, name: "read_file", args: { file_path: `/skills/${name}/SKILL.md`, offset: 0, limit: 200 } }))
        : index === 2 ? [{ id: "targeted-instructions", name: "read_file", args: { file_path: "/skills/go-investigation/SKILL.md", offset: 7, limit: 1 } }]
          : index === 3 ? [{ id: "bounded-source", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 2 } }]
            : [{ id: "bounded-patch", name: "propose_patch", args: patchArgs }], index === 1 ? "Load relevant pinned playbooks" : "Keep the latest native thought")), { status: 200 });
    };
    const caller = async (id: string, name: string) => {
      effects++;
      return { call_id: id, status: "ok" as const, value: name === "repo_read" ? { path: "internal/worker/job.go", digest: "pinned-source", start_line: 1, content: "package worker\nfunc old() {}" }
        : { terminal: true, outcome: "patch_ready", code: "PATCH_READY" } };
    };
    expect(await investigate(start, caller, new AbortController().signal, fetcher)).toMatchObject({ status: "completed", code: "PATCH_READY", model_requests: 4, tool_steps: 6 });
    expect(wires).toHaveLength(4); expect(effects).toBe(2);
    expect(previews).toBeGreaterThanOrEqual(4); expect(previews).toBeLessThanOrEqual(8);
    for (const wire of wires) expect(Buffer.byteLength(JSON.stringify(wire)) + 1400).toBeLessThanOrEqual(6000);
    const instruction = (wires[2]!.messages as Array<{ tool_call_id?: string; content: unknown }>).find((message) => message.tool_call_id === "targeted-instructions")!;
    expect(JSON.stringify(instruction.content)).toContain("Trace the observed failure to a specific path");
    const saver = checkpointSaver(start);
    try {
      const stored = (await saver.getTuple(config(start)))!;
      const messages = stored.checkpoint.channel_values.messages as BaseMessage[];
      expect(messages.filter(isAIMessage).map((message) => message.additional_kwargs.reasoning_content)).toContain("Load relevant pinned playbooks");
      expect(JSON.stringify(stored.checkpoint.channel_values.files)).toContain("Untrusted".toLowerCase());
    } finally { if ("end" in saver) await saver.end(); }
    expect(await investigate(start, caller, new AbortController().signal, fetcher)).toMatchObject({ status: "completed", model_requests: 4, tool_steps: 6 });
    expect(wires).toHaveLength(4); expect(effects).toBe(2);
  }, 30000);

  it("allows only explicit internal candidate correction and checkpoints its counter", async () => {
    const start = await fixture(); start.scope.profile = "internal"; start.scope.limits.max_candidate_count = 2;
    const gateway = await stub((request, index) => ({ body: completion(String(request.body.model), [{ id: `candidate-${index}`, name: "propose_patch", args: { ...patchArgs, patch: patchArgs.patch.replace("+fixed", `+fixed${index}`) } }]) }));
    start.transport.model_gateway_url = gateway.url;
    let candidates = 0;
    try {
      const result = await investigate(start, async (id) => {
        candidates++;
        return { call_id: id, status: "ok", value: candidates === 1
          ? { terminal: false, outcome: "candidate_feedback", code: "TESTS_FAILED", candidate_count: 1, max_candidates: 2, base_restored: true, reason: "offline regression remains red" }
          : { terminal: true, outcome: "patch_ready", code: "PATCH_READY" } };
      }, new AbortController().signal);
      expect(result).toMatchObject({ status: "completed", model_requests: 2, tool_steps: 2 });
      expect(candidates).toBe(2); expect(gateway.requests).toHaveLength(2);
      const saver = checkpointSaver(start);
      try { expect((await saver.getTuple(config(start)))!.checkpoint.channel_values.candidate_count).toBe(2); }
      finally { if ("end" in saver) await saver.end(); }
    } finally { await gateway.close(); }
  }, 30000);

  it("rejects candidate feedback in the final smoke profile without another model call", async () => {
    const start = await fixture(); start.scope.profile = "final-smoke";
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ id: "smoke-candidate", name: "propose_patch", args: patchArgs }]) }));
    start.transport.model_gateway_url = gateway.url;
    try {
      const result = await investigate(start, async (id) => ({ call_id: id, status: "ok", value: { terminal: false, outcome: "candidate_feedback", code: "TESTS_FAILED", candidate_count: 1, max_candidates: 2, base_restored: true, reason: "must not retry smoke" } }), new AbortController().signal);
      expect(result.status).toBe("failed"); expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  }, 30000);

  it("runs a separately authorized reviewer with only virtual reads and shared request ordinals", async () => {
    const start = await fixture(); start.scope.profile = "internal";
    const proof: CandidateProof = { candidate_digest: "d".repeat(64), patch: patchArgs.patch, recipe_id: start.scope.recipe_ids[0]!, baseline_exit_code: 1, candidate_exit_code: 0, test_output: "offline regression PASS" };
    const artifacts = new Artifacts(start.scope.attempt_id); const path = artifacts.put(JSON.stringify(proof, null, 2));
    const gateway = await stub((request, index) => {
      if (index === 1) return { body: completion(String(request.body.model), [{ id: "review-read", name: "read_file", args: { file_path: path } }]) };
      const body = completion(String(request.body.model), []);
      (body.choices as Array<{ message: { content: string } }>)[0]!.message.content = JSON.stringify({ concerns: [] });
      return { body };
    });
    start.transport.model_gateway_url = gateway.url;
    try {
      const authority = { enabled: true, stage: "reviewer", depth: 1, reviewer_index: 0, first_request_ordinal: 1 };
      expect(await invokeReadOnlyReviewer(start, authority, proof, new AbortController().signal)).toMatchObject({ status: "completed", code: "REVIEW_COMPLETE", model_requests: 2, tool_steps: 1, concerns: [] });
      expect(gateway.requests.map((request) => request.headers["x-triovexa-request-ordinal"])).toEqual(["1", "2"]);
      for (const request of gateway.requests) expect((request.body.tools as Array<{ function: { name: string } }>).map((item) => item.function.name)).toEqual(["read_file"]);
      expect(await invokeReadOnlyReviewer(start, authority, proof, new AbortController().signal)).toMatchObject({ status: "completed", model_requests: 2 });
      expect(gateway.requests).toHaveLength(2);
    } finally { await gateway.close(); }
  }, 30000);

  it.each(["propose_patch", "run_test_recipe", "task"])("reviewer rejects %s without mutation or another request", async (name) => {
    const start = await fixture(); start.scope.profile = "internal";
    const proof: CandidateProof = { candidate_digest: "d".repeat(64), patch: patchArgs.patch, recipe_id: start.scope.recipe_ids[0]!, baseline_exit_code: 1, candidate_exit_code: 0, test_output: "offline regression PASS" };
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ id: "forbidden-review-call", name, args: name === "propose_patch" ? patchArgs : { recipe_id: "worker-regression" } }]) }));
    start.transport.model_gateway_url = gateway.url;
    try {
      expect(await invokeReadOnlyReviewer(start, { enabled: true, stage: "reviewer", depth: 1, reviewer_index: 0, first_request_ordinal: 1 }, proof, new AbortController().signal)).toMatchObject({ status: "failed", tool_steps: 0 });
      expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  }, 30000);

  it("preserves proof through a checkpoint-table backup/restore and idempotent upgrade", async () => {
    const start = await fixture();
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ id: "backup-final", name: "cannot_determine", args: { reason: "offline result" } }]) }));
    start.transport.model_gateway_url = gateway.url;
    const caller = async (id: string) => ({ call_id: id, status: "ok" as const, value: { terminal: true, outcome: "blocked", code: "INSUFFICIENT_EVIDENCE" } });
    try {
      await investigate(start, caller, new AbortController().signal);
      const tables = ["checkpoints", "checkpoint_blobs", "checkpoint_writes"];
      const backups = [];
      for (const table of tables) backups.push((await admin.query(`SELECT * FROM "${schema}"."${table}" WHERE thread_id = $1`, [start.scope.checkpoint_thread])).rows);
      for (const table of tables) await admin.query(`DELETE FROM "${schema}"."${table}" WHERE thread_id = $1`, [start.scope.checkpoint_thread]);
      for (const [index, table] of tables.entries()) for (const row of backups[index]!) {
        const columns = Object.keys(row);
        await admin.query(`INSERT INTO "${schema}"."${table}" (${columns.map((column) => pg.escapeIdentifier(column)).join(",")}) VALUES (${columns.map((_, i) => `$${i + 1}`).join(",")})`, Object.values(row));
      }
      // Replay the final SDK migration, which is safe with existing data.
      await admin.query(`DELETE FROM "${schema}".checkpoint_migrations WHERE v = 4`);
      await initializeCheckpoint({ adminDSN: databaseURL!, schema, role, password });
      expect((await admin.query(`SELECT max(v) AS v FROM "${schema}".checkpoint_migrations`)).rows[0].v).toBe(4);
      expect((await admin.query(`SELECT value FROM "${domainSchema}".provider_secrets`)).rows).toEqual([{ value: "dummy-domain-secret" }]);
      expect(await investigate(start, caller, new AbortController().signal)).toMatchObject({ status: "blocked", code: "INSUFFICIENT_EVIDENCE", model_requests: 1 });
      expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  }, 30000);
});
