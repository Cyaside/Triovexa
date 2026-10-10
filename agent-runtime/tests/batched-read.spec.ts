import { describe, expect, it } from "vitest";
import { investigate } from "../src/runtime/engine.js";
import { completion, patchArgs, startFixture, stub } from "./fixture.js";

describe("batched native source reads", () => {
  it("keeps candidate state unchanged for concurrent reads before a verified proposal", async () => {
    const gateway = await stub((request, index) => ({ body: completion(String(request.body.model), index === 1
      ? [1, 2, 3].map((line) => ({ id: `read-${line}`, name: "repo_read", args: { path: "internal/worker/job.go", start_line: line, end_line: line } }))
      : [{ id: "patch", name: "propose_patch", args: patchArgs }]) }));
    try {
      const start = await startFixture();
      start.transport.model_gateway_url = gateway.url;
      const calls: string[] = [];
      const result = await investigate(start, async (id, name) => {
        calls.push(id);
        return { call_id: id, status: "ok", value: name === "propose_patch"
          ? { terminal: true, outcome: "patch_ready", code: "PATCH_READY" }
          : { path: "internal/worker/job.go", content: "approved source line" } };
      }, new AbortController().signal);
      expect(result).toMatchObject({ status: "completed", code: "PATCH_READY", model_requests: 2, tool_steps: 4 });
      expect(calls).toEqual(["read-1", "read-2", "read-3", "patch"]);
      expect(gateway.requests).toHaveLength(2);
      expect(gateway.requests[0]?.body.parallel_tool_calls).toBe(false);
      const messages = gateway.requests[1]?.body.messages as Array<{ role: string; tool_call_id?: string }>;
      expect(messages.filter((message) => message.role === "tool").map((message) => message.tool_call_id)).toEqual(["read-1", "read-2", "read-3"]);
    } finally { await gateway.close(); }
  });
});
