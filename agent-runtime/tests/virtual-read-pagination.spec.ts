import { StateBackend } from "deepagents";
import { describe, expect, it } from "vitest";
import { Artifacts } from "../src/context/artifacts.js";
import { VirtualBackend } from "../src/context/virtual-backend.js";
import { investigate } from "../src/runtime/engine.js";
import { completion, startFixture, stub } from "./fixture.js";

describe("virtual read output pagination", () => {
  it("returns complete UTF-8 source lines and contiguous continuation offsets", async () => {
    const source = Array.from({ length: 150 }, (_, index) => `${index}: ${"漢🙂".repeat(25)}`).join("\n");
    const artifacts = new Artifacts("paged-attempt"); const path = artifacts.put(source);
    const backend = new VirtualBackend(new StateBackend({ state: { files: artifacts.snapshot() } }), "paged-attempt", new AbortController().signal);
    let offset = 0; const windows: string[] = [];
    do {
      const result = await backend.read(path, offset, 200);
      expect(result.error).toBeUndefined();
      expect(typeof result.content).toBe("string");
      expect(Buffer.byteLength(String(result.content))).toBeLessThanOrEqual(8192 - 128);
      expect(result.startLine).toBe(offset + 1);
      expect(result.totalLines).toBe(150);
      windows.push(String(result.content));
      if (result.nextOffset === undefined) break;
      expect(result.nextOffset).toBe(result.endLine);
      expect(result.nextOffset).toBeGreaterThan(offset);
      offset = result.nextOffset;
    } while (true);
    expect(windows.length).toBeGreaterThan(1);
    expect(windows.join("\n")).toBe(source);
    expect((await backend.readRaw(path)).data).toEqual(artifacts.snapshot()[path]);
  });

  it("refuses an oversized first line instead of hiding its inaccessible remainder", async () => {
    const artifacts = new Artifacts("long-line-attempt");
    const path = artifacts.put("漢".repeat(2800) + "\nreachable second line");
    const backend = new VirtualBackend(new StateBackend({ state: { files: artifacts.snapshot() } }), "long-line-attempt", new AbortController().signal);
    expect(await backend.read(path, 0, 200)).toEqual({ error: "VIRTUAL_LINE_LIMIT" });
    expect((await backend.read(path, 1, 1)).content).toBe("reachable second line");
    expect((await backend.read(path, Number.MAX_SAFE_INTEGER + 1, 1)).error).toBe("VIRTUAL_READ_DENIED");
  });

  it("returns preceding complete lines and resumes at an oversized line without skipping it", async () => {
    const artifacts = new Artifacts("no-skip-attempt");
    const path = artifacts.put("readable\n" + "x".repeat(9000) + "\nlast");
    const backend = new VirtualBackend(new StateBackend({ state: { files: artifacts.snapshot() } }), "no-skip-attempt", new AbortController().signal);
    expect(await backend.read(path, 0, 3)).toMatchObject({ content: "readable", startLine: 1, endLine: 1, totalLines: 3, nextOffset: 1 });
    expect(await backend.read(path, 1, 2)).toEqual({ error: "VIRTUAL_LINE_LIMIT" });
  });

  it("bounds the native rendered ToolMessage and keeps the continuation in the final SDK payload", async () => {
    let artifact = "";
    const source = Array.from({ length: 150 }, (_, index) => `${index}: ${"source ".repeat(25)}`).join("\n");
    const gateway = await stub((request, index) => {
      if (index === 2) {
        const messages = request.body.messages as Array<{ role: string; content: string }>;
        artifact = JSON.parse(messages.find((message) => message.role === "tool")!.content).artifact;
      }
      return { body: completion(String(request.body.model), index === 1
        ? [{ id: "offload-source", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 150 } }]
        : index === 2 ? [{ id: "page-source", name: "read_file", args: { file_path: artifact, offset: 0, limit: 200 } }]
          : [{ id: "stop-pagination", name: "cannot_determine", args: { reason: "Source fixture remains inconclusive" } }]) };
    });
    try {
      const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
      start.scope.limits.max_context_bytes = 24000;
      const result = await investigate(start, async (id, name) => ({ call_id: id, status: "ok", value: name === "repo_read"
        ? { path: "internal/worker/job.go", digest: "approved-source", start_line: 1, content: source }
        : { terminal: true, outcome: "blocked", code: "INSUFFICIENT_EVIDENCE" } }), new AbortController().signal);
      expect(result).toMatchObject({ code: "INSUFFICIENT_EVIDENCE", model_requests: 3, tool_steps: 3 });
      expect(gateway.requests).toHaveLength(3);
      const messages = gateway.requests[2]!.body.messages as Array<{ role: string; content: string | Array<{ text: string }>; tool_call_id?: string }>;
      const resultMessage = messages.find((message) => message.tool_call_id === "page-source")!;
      const text = typeof resultMessage.content === "string" ? resultMessage.content : resultMessage.content.map((part) => part.text).join("\n");
      expect(Buffer.byteLength(text)).toBeLessThanOrEqual(8192);
      expect(text).toMatch(/^@@ lines 1-\d+ of \d+ \| next offset \d+ @@/);
      expect(text).toContain("approved-source");
      expect(text).toContain("0: source");
      expect(JSON.stringify(gateway.requests[2]!.body)).not.toContain("VIRTUAL_RANGE_LIMIT");
      expect(Buffer.byteLength(JSON.stringify(gateway.requests[2]!.body))).toBeLessThanOrEqual(start.scope.limits.max_input_bytes);
    } finally { await gateway.close(); }
  });
});
