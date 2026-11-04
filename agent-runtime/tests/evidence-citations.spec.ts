import { describe, expect, it } from "vitest";
import { schemasForEvidence } from "../src/bridge/schema.js";
import { investigate } from "../src/runtime/engine.js";
import { completion, patchArgs, startFixture, stub } from "./fixture.js";

describe("incident evidence citations", () => {
  it("exposes only exact available incident IDs as model choices", () => {
    const schema = schemasForEvidence({ entries: [{ id: "log-1", status: "available" }, { id: "missing", status: "missing" }, { id: "stale", status: "stale" }] }).propose_patch;
    expect(schema.safeParse(patchArgs).success).toBe(true);
    for (const id of ["missing", "stale", "untrusted:incident:log-1", "repo:read:worker.py:1-3@digest"]) {
      expect(schema.safeParse({ ...patchArgs, evidence_ids: [id] }).success).toBe(false);
    }
    expect(() => schemasForEvidence({ entries: [] })).toThrowError("CONTRACT_INVALID");
  });

  it("rejects manufactured source/prefixed citations before Go execution without model retry", async () => {
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ id: "bad-citation", name: "propose_patch", args: { ...patchArgs, evidence_ids: ["untrusted:incident:log-1"] } }]) }));
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      let effects = 0;
      const result = await investigate(start, async () => { effects++; throw new Error("must not execute"); }, new AbortController().signal);
      expect(result).toMatchObject({ status: "failed", code: "TOOL_ARGUMENT_INVALID", model_requests: 1, tool_steps: 1 });
      expect(effects).toBe(0); expect(gateway.requests).toHaveLength(1);
      const tool = (gateway.requests[0]!.body.tools as Array<{ function: { name: string; parameters: unknown } }>).find((item) => item.function.name === "propose_patch")!;
      expect(tool.function.parameters).toMatchObject({ properties: { evidence_ids: { items: { enum: ["log-1"] } } } });
    } finally { await gateway.close(); }
  });
});
