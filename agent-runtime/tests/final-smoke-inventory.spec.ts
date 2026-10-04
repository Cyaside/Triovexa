import { AIMessage, HumanMessage, ToolMessage } from "@langchain/core/messages";
import { describe, expect, it } from "vitest";
import { MODEL_TOOLS } from "../src/bridge/schema.js";
import { ModelTransport } from "../src/runtime/model.js";
import { writerToolsForProfile } from "../src/runtime/tool-inventory.js";
import { completion, startFixture } from "./fixture.js";

const expected = ["repo_read", "propose_patch", "cannot_determine", "read_file"];
const definitions = (names: readonly string[]) => names.map((name) => ({ type: "function", function: { name, parameters: { type: "object" } } }));

describe("final-smoke writer inventory", () => {
  it("narrows only final-smoke and returns independent immutable selections", () => {
    expect(writerToolsForProfile("final-smoke")).toEqual(expected);
    for (const profile of ["internal", "offline-fixture"] as const) expect(writerToolsForProfile(profile)).toEqual(MODEL_TOOLS);
    const selection = [...writerToolsForProfile("final-smoke")]; selection.pop();
    expect(writerToolsForProfile("final-smoke")).toEqual(expected);
  });

  it("preserves 2000 bytes of native reasoning and complete tool-call history inside the unchanged wire limit", async () => {
    const start = await startFixture(); start.scope.profile = "final-smoke"; start.scope.limits.max_input_bytes = 5488;
    const reasoning = "r".repeat(2000);
    const call = { id: "read-one", name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 5 }, type: "tool_call" as const };
    const history = [new HumanMessage("Approved source/evidence scope"),
      new AIMessage({ content: "", additional_kwargs: { reasoning_content: reasoning }, tool_calls: [call] }),
      new ToolMessage({ content: "package worker", tool_call_id: call.id, name: call.name })];
    let wire: Record<string, unknown> | undefined;
    const transport = new ModelTransport(start, new AbortController().signal, async (_input, init) => {
      wire = JSON.parse(String(init?.body));
      return new Response(JSON.stringify(completion(start.transport.model, [{ id: "stop-one", name: "cannot_determine", args: { reason: "fixture complete" } }])), { status: 200 });
    });
    transport.prepare(history, 2);
    const body = { model: start.transport.model, stream: false, parallel_tool_calls: false, max_tokens: 1500,
      tools: definitions(expected), messages: [{ role: "user", content: "Approved source/evidence scope" },
        { role: "assistant", content: null, tool_calls: [{ id: call.id, type: "function", function: { name: call.name, arguments: JSON.stringify(call.args) } }] },
        { role: "tool", content: "package worker", tool_call_id: call.id }] };
    await transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) });
    expect(transport.dispatched).toBe(1);
    expect(wire).toBeDefined();
    const messages = wire!.messages as Array<{ role: string; reasoning_content?: string; tool_call_id?: string }>;
    expect(messages.find((message) => message.role === "assistant")?.reasoning_content).toBe(reasoning);
    expect(messages.find((message) => message.role === "tool")?.tool_call_id).toBe(call.id);
    expect(Buffer.byteLength(JSON.stringify(wire))).toBeLessThanOrEqual(5488);
    expect((wire!.tools as Array<{ function: { name: string } }>).map((tool) => tool.function.name)).toEqual(expected);
  });

  it.each(["repo_list", "repo_search", "run_test_recipe"])("rejects excluded response tool %s before Go effects", async (name) => {
    const start = await startFixture(); start.scope.profile = "final-smoke";
    const transport = new ModelTransport(start, new AbortController().signal, async () => new Response(JSON.stringify(completion(start.transport.model, [{ id: "excluded-one", name, args: {} }])), { status: 200 }));
    transport.prepare([new HumanMessage("scope")], 1);
    const body = { model: start.transport.model, stream: false, max_tokens: 1500, tools: definitions(expected), messages: [{ role: "user", content: "scope" }] };
    await expect(transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) })).rejects.toThrow("CALL_ID_INVALID");
    expect(transport.dispatched).toBe(1);
  });

  it.each([{ names: expected.slice(0, 3) }, { names: [...expected, "repo_list"] }])("rejects missing or widened request inventory before dispatch", async ({ names }) => {
    const start = await startFixture(); start.scope.profile = "final-smoke";
    let calls = 0;
    const transport = new ModelTransport(start, new AbortController().signal, async () => { calls++; throw new Error("must not dispatch"); });
    transport.prepare([new HumanMessage("scope")], 1);
    const body = { model: start.transport.model, stream: false, max_tokens: 1500, tools: definitions(names), messages: [{ role: "user", content: "scope" }] };
    await expect(transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) })).rejects.toThrow("TOOL_INVENTORY_INVALID");
    expect(calls).toBe(0); expect(transport.dispatched).toBe(0);
  });
});
