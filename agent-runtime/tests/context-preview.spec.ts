import { AIMessage, HumanMessage, ToolMessage } from "@langchain/core/messages";
import { describe, expect, it } from "vitest";
import { Artifacts } from "../src/context/artifacts.js";
import { offloadWireToolResults } from "../src/context/wire-offload.js";
import { ModelTransport } from "../src/runtime/model.js";
import { writerToolsForProfile } from "../src/runtime/tool-inventory.js";
import { investigate } from "../src/runtime/engine.js";
import { completion, startFixture } from "./fixture.js";

type Captured = { preview: boolean; body: Record<string, unknown>; headers: Headers };
const response = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
const definitions = (names: readonly string[]) => names.map((name) => ({ type: "function", function: { name, parameters: { type: "object" } } }));

describe("gateway-measured context preview", () => {
  it("offloads only tool text, retains exact source/reasoning/call pairs and remeasures the final SDK payload once", async () => {
    const start = await startFixture(); start.transport.input_budget_mode = "gateway-preview";
    const artifacts = new Artifacts(start.scope.attempt_id); const requests: Captured[] = [];
    const source = Array.from({ length: 150 }, (_, index) => `${index}: ${"source line ".repeat(6)}`).join("\n");
    const toolText = JSON.stringify({ path: "internal/worker/job.go", digest: "pinned-source", start_line: 1, content: source });
    const reasoning = "r".repeat(2000);
    const call = { id: "source-one", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 150 }, type: "tool_call" as const };
    const history = [new HumanMessage("Approved scope/evidence unchanged"), new AIMessage({ content: "", tool_calls: [call], additional_kwargs: { reasoning_content: reasoning } }), new ToolMessage({ content: toolText, tool_call_id: call.id, name: call.name })];
    const transport = new ModelTransport(start, new AbortController().signal, async (_input, init) => {
      const body = JSON.parse(String(init?.body)); const headers = new Headers(init?.headers); const preview = headers.get("X-Triovexa-Context-Preview") === "1";
      requests.push({ preview, body, headers });
      // Deterministic local admission fixture. The Go integration tests prove
      // the real model template bound; TS must obey the returned measurement.
      return response(preview ? { input_bound: Buffer.byteLength(JSON.stringify(body)), max_input_tokens: 6000 }
        : completion(start.transport.model, [{ id: "stop-one", name: "cannot_determine", args: { reason: "Fixture ends here" } }]));
    }, "writer", artifacts);
    transport.prepare(history, 2);
    const body = { model: start.transport.model, stream: false, max_tokens: 1500, parallel_tool_calls: false,
      tools: definitions(writerToolsForProfile(start.scope.profile)), messages: [{ role: "user", content: "Approved scope/evidence unchanged" },
        { role: "assistant", content: null, tool_calls: [{ id: call.id, type: "function", function: { name: call.name, arguments: JSON.stringify(call.args) } }] },
        { role: "tool", content: toolText, tool_call_id: call.id }] };
    await transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) });
    expect(requests.map((request) => request.preview)).toEqual([true, true, false]);
    expect(transport.dispatched).toBe(1);
    expect(requests[2]!.body).toEqual(requests[1]!.body);
    expect(Buffer.byteLength(JSON.stringify(requests[2]!.body))).toBeLessThanOrEqual(6000);
    const messages = requests[2]!.body.messages as Array<{ role: string; content?: string; reasoning_content?: string; tool_calls?: unknown; tool_call_id?: string }>;
    expect(messages[0]!.content).toBe(body.messages[0]!.content);
    expect(messages[1]!.reasoning_content).toBe(reasoning);
    expect(messages[1]!.tool_calls).toEqual(body.messages[1]!.tool_calls);
    expect(messages[2]!.tool_call_id).toBe(call.id);
    const reference = JSON.parse(messages[2]!.content!);
    expect(artifacts.snapshot()[reference.original]!.content).toBe(toolText);
    expect(artifacts.snapshot()[reference.artifact]!.content).toContain("pinned-source");
    expect(artifacts.snapshot()[reference.artifact]!.content).toContain("\n0: source line");
    expect(body.messages[2]!.content).toBe(toolText);
    expect(history[2]!.content).toBe(toolText);
    for (const captured of requests) {
      expect(captured.headers.get("X-Triovexa-Request-Ordinal")).toBe("2");
      expect(captured.headers.get("X-Triovexa-Case-ID")).toBe(start.scope.case_id);
      expect(captured.headers.get("X-Triovexa-Attempt-ID")).toBe(start.scope.attempt_id);
    }
    expect(requests[2]!.headers.has("X-Triovexa-Context-Preview")).toBe(false);
  });

  it("preserves native text block shape and stores every replaced block byte for byte", () => {
    const artifacts = new Artifacts("block-attempt"); const original = "@@ lines 1-8 of 8 @@\n" + "trusted skill instruction\n".repeat(40);
    const messages = [{ role: "system", content: original }, { role: "user", content: original }, { role: "assistant", content: original, reasoning_content: original },
      { role: "tool", tool_call_id: "native-skill", content: [{ type: "text", text: original }, { type: "text", text: "short result" }] }];
    expect(offloadWireToolResults(messages, artifacts)).toBe(1);
    expect(messages.slice(0, 3).map((message) => message.content)).toEqual([original, original, original]);
    const blocks = messages[3]!.content as Array<{ type: string; text: string }>;
    expect(blocks.map((block) => block.type)).toEqual(["text", "text"]);
    expect(blocks[1]!.text).toBe("short result");
    const reference = JSON.parse(blocks[0]!.text);
    expect(reference.untrusted).toBe(true);
    expect(artifacts.snapshot()[reference.artifact]!.content).toBe(original);
  });

  it("fails before inference when the second measurement is still too large", async () => {
    const start = await startFixture(); start.transport.input_budget_mode = "gateway-preview";
    const artifacts = new Artifacts(start.scope.attempt_id); let previews = 0; let modelCalls = 0;
    const transport = new ModelTransport(start, new AbortController().signal, async (_input, init) => {
      if (new Headers(init?.headers).get("X-Triovexa-Context-Preview") === "1") { previews++; return response({ input_bound: 6500, max_input_tokens: 6000 }); }
      modelCalls++; throw new Error("No inference authorized");
    }, "writer", artifacts);
    transport.prepare([new HumanMessage("scope")], 1);
    const body = { model: start.transport.model, stream: false, max_tokens: 1500, tools: definitions(writerToolsForProfile(start.scope.profile)), messages: [
      { role: "user", content: "Approved scope" },
      { role: "assistant", content: null, tool_calls: [{ id: "large-result", type: "function", function: { name: "repo_read", arguments: JSON.stringify({ path: "internal/worker/job.go", start_line: 1, end_line: 2 }) } }] },
      { role: "tool", tool_call_id: "large-result", content: "untrusted output ".repeat(100) }] };
    await expect(transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) })).rejects.toThrow("CONTEXT_LIMIT");
    expect(previews).toBe(2); expect(modelCalls).toBe(0); expect(transport.dispatched).toBe(0);
  });

  it.each([{ input_bound: 1, max_input_tokens: 6000, usage: {} }, { input_bound: -1, max_input_tokens: 6000 }, { input_bound: 1.5, max_input_tokens: 6000 }, { input_bound: 1, max_input_tokens: "6000" }])("rejects an invalid preview contract without inference", async (preview) => {
    const start = await startFixture(); start.transport.input_budget_mode = "gateway-preview";
    const transport = new ModelTransport(start, new AbortController().signal, async () => response(preview), "writer", new Artifacts(start.scope.attempt_id));
    transport.prepare([new HumanMessage("scope")], 1);
    const body = { model: start.transport.model, stream: false, max_tokens: 1500, tools: definitions(writerToolsForProfile(start.scope.profile)), messages: [{ role: "user", content: "scope" }] };
    await expect(transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) })).rejects.toThrow("CONTEXT_PREVIEW_INVALID");
    expect(transport.dispatched).toBe(0);
  });

  it("does not add a preview or change content for an existing manually verified wire contract", async () => {
    const start = await startFixture(); let calls = 0;
    const transport = new ModelTransport(start, new AbortController().signal, async (_input, init) => {
      calls++; expect(new Headers(init?.headers).has("X-Triovexa-Context-Preview")).toBe(false);
      return response(completion(start.transport.model, [{ id: "stop-manual", name: "cannot_determine", args: { reason: "Fixture ends" } }]));
    });
    transport.prepare([new HumanMessage("scope")], 1);
    const body = { model: start.transport.model, stream: false, max_tokens: 1500, tools: definitions(writerToolsForProfile(start.scope.profile)), messages: [{ role: "user", content: "scope" }] };
    await transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) });
    expect(calls).toBe(1); expect(transport.dispatched).toBe(1);
  });

  it("checkpoints preview artifacts before native read_file and retrieves relevant source lines without a hidden model call", async () => {
    const start = await startFixture(); start.transport.input_budget_mode = "gateway-preview";
    const source = Array.from({ length: 60 }, (_, index) => `${index}: ${"source ".repeat(12)}`).join("\n");
    const requests: Captured[] = []; let actual = 0; let artifact = "";
    const fetcher: typeof fetch = async (_input, init) => {
      const body = JSON.parse(String(init?.body)); const headers = new Headers(init?.headers); const preview = headers.get("X-Triovexa-Context-Preview") === "1";
      requests.push({ preview, body, headers });
      if (preview) return response({ input_bound: Buffer.byteLength(JSON.stringify(body)), max_input_tokens: 6000 });
      actual++;
      if (actual === 2) {
        const messages = body.messages as Array<{ role: string; content: string }>;
        artifact = JSON.parse(messages.find((message) => message.role === "tool")!.content).artifact;
      }
      return response(completion(start.transport.model, actual === 1
        ? [{ id: "source-first", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 60 } }]
        : actual === 2 ? [{ id: "source-page", name: "read_file", args: { file_path: artifact, offset: 8, limit: 2 } }]
          : [{ id: "stop-preview", name: "cannot_determine", args: { reason: "Source remains inconclusive" } }], actual === 1 ? "Pinned evidence must be inspected" : undefined));
    };
    const result = await investigate(start, async (id, name) => ({ call_id: id, status: "ok", value: name === "repo_read"
      ? { path: "internal/worker/job.go", digest: "preview-source", start_line: 1, content: source }
      : { terminal: true, outcome: "blocked", code: "INSUFFICIENT_EVIDENCE" } }), new AbortController().signal, fetcher);
    expect(result).toMatchObject({ code: "INSUFFICIENT_EVIDENCE", model_requests: 3, tool_steps: 3 });
    expect(actual).toBe(3);
    const sent = requests.filter((request) => !request.preview);
    expect(sent).toHaveLength(3);
    for (const request of sent) expect(Buffer.byteLength(JSON.stringify(request.body))).toBeLessThanOrEqual(6000);
    const messages = sent[2]!.body.messages as Array<{ role: string; tool_call_id?: string; content: string | Array<{ text: string }> }>;
    const read = messages.find((message) => message.tool_call_id === "source-page")!;
    const text = typeof read.content === "string" ? read.content : read.content.map((part) => part.text).join("\n");
    expect(text).toContain("0: source");
    expect(text).toContain("1: source");
    expect(text).not.toContain("File data not found");
    expect(requests.filter((request) => request.preview)).toHaveLength(5);
  });
});
