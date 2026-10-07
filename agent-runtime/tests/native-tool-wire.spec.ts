import { describe, it, expect } from "vitest";
import { investigate } from "../src/runtime/engine.js";
import { MODEL_TOOLS, validateStart } from "../src/bridge/schema.js";
import { completion, patchArgs, startFixture, stub } from "./fixture.js";

describe("native Chat Completions runtime", () => {
  it("rejects a prompt contract unsupported by this executable", async () => {
    const start = await startFixture(); start.scope.prompt_version = "repair-native-v3";
    expect(() => validateStart(start)).toThrowError("PROMPT_VERSION_UNAVAILABLE");
  });
  it("uses exactly seven tools and stops after the Go-verified terminal proposal", async () => {
    const gateway = await stub((request, index) => ({ body: completion(String(request.body.model), index === 1 ? [{ id: "read-1", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 20 } }] : [{ id: "patch-1", name: "propose_patch", args: patchArgs }], index === 1 ? "offline-private-reasoning" : undefined) }));
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      const tools: string[] = [];
      const result = await investigate(start, async (id, name) => {
        tools.push(name);
        return { call_id: id, status: "ok", value: name === "propose_patch" ? { terminal: true, outcome: "patch_ready", code: "PATCH_READY" } : { path: "internal/worker/job.go", digest: "approved-digest", start_line: 1, content: "package worker\nfunc parse() {}" } };
      }, new AbortController().signal);
      expect(result).toMatchObject({ status: "completed", code: "PATCH_READY", model_requests: 2, tool_steps: 2 });
      expect(tools).toEqual(["repo_read", "propose_patch"]);
      expect(gateway.requests).toHaveLength(2);
      for (const [index, request] of gateway.requests.entries()) {
        expect(request.path).toBe("/v1/chat/completions");
        expect(request.body.stream).toBe(false);
        expect(request.body.parallel_tool_calls).toBe(false);
        expect(request.body.max_tokens).toBe(1500);
        expect((request.body.tools as Array<{ function: { name: string } }>).map((tool) => tool.function.name).sort()).toEqual([...MODEL_TOOLS].sort());
        const proposal = (request.body.tools as Array<{ function: { name: string; description: string } }>).find((tool) => tool.function.name === "propose_patch")!;
        expect(proposal.function.description).toContain("Text Git diff starts diff --git");
        expect(request.headers["x-triovexa-request-ordinal"]).toBe(String(index + 1));
        expect(JSON.stringify(request.body)).not.toContain(start.transport.capability);
        expect(JSON.stringify(request.body)).not.toContain("model_gateway_url");
      }
      const messages = gateway.requests[1]!.body.messages as Array<{ role: string; tool_call_id?: string; reasoning_content?: string }>;
      expect(messages.find((message) => message.role === "tool")?.tool_call_id).toBe("read-1");
      expect(messages.find((message) => message.role === "assistant")?.reasoning_content).toBe("offline-private-reasoning");
    } finally { await gateway.close(); }
  });

  it.each([429, 500])("does not retry HTTP %i or return provider error secrets", async (status) => {
    const gateway = await stub(() => ({ status, body: { error: { message: "api_key=do-not-leak" } } }));
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      const result = await investigate(start, async () => { throw new Error("no tool expected"); }, new AbortController().signal);
      expect(result.status).toBe("failed");
      expect(JSON.stringify(result)).not.toContain("do-not-leak");
      expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  });

  it.each(["unknown_tool", "task", "execute", "write_file"])("rejects %s before tools execute", async (name) => {
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ name, args: { command: "unapproved" } }]) }));
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      let effects = 0;
      const result = await investigate(start, async () => { effects++; throw new Error("must not execute"); }, new AbortController().signal);
      expect(result.status).toBe("failed"); expect(effects).toBe(0); expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  });

  it("treats invalid tool arguments as terminal instead of requesting schema correction", async () => {
    const gateway = await stub((request) => ({ body: completion(String(request.body.model), [{ name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 20, extra_scope: "unapproved" } }]) }));
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      let effects = 0;
      const result = await investigate(start, async () => { effects++; throw new Error("must not execute"); }, new AbortController().signal);
      expect(result.status).toBe("failed"); expect(effects).toBe(0); expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  });

  it.each(["missing", "duplicate"])("rejects %s native tool call IDs before Go side effects", async (invalid) => {
    const gateway = await stub((request) => {
      const body = completion(String(request.body.model), [
        { id: "same-id", name: "repo_list", args: { prefix: "internal/worker" } },
        { id: "other-id", name: "repo_search", args: { prefix: "internal/worker", query: "worker" } },
      ]);
      const calls = (body.choices as Array<{ message: { tool_calls: Array<{ id?: string }> } }>)[0]!.message.tool_calls;
      if (invalid === "missing") delete calls[0]!.id; else calls[1]!.id = "same-id";
      return { body };
    });
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      let effects = 0;
      expect(await investigate(start, async () => { effects++; throw new Error("must not execute"); }, new AbortController().signal)).toMatchObject({ status: "failed", code: "CALL_ID_INVALID" });
      expect(effects).toBe(0); expect(gateway.requests).toHaveLength(1);
    } finally { await gateway.close(); }
  });
});
