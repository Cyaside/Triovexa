import { HumanMessage } from "@langchain/core/messages";
import { describe, expect, it } from "vitest";
import { MODEL_TOOLS, RuntimeFailure, runtimeFailureCode } from "../src/bridge/schema.js";
import { ModelTransport } from "../src/runtime/model.js";
import { investigate } from "../src/runtime/engine.js";
import { startFixture } from "./fixture.js";

const codes = ["CONTEXT_LIMIT", "BUDGET_EXHAUSTED", "PROVIDER_DISPATCH_UNCERTAIN", "USAGE_UNKNOWN",
  "OFFLINE_EGRESS_DENIED", "PRICING_UNKNOWN", "BILLING_UNBOUNDED", "MODEL_DISPATCH_BLOCKED"];

async function deniedTransport(content: string, status = 409): Promise<{ response?: Response; error?: unknown; calls: number }> {
  const start = await startFixture();
  let calls = 0;
  const transport = new ModelTransport(start, new AbortController().signal, async () => {
    calls++; return new Response(content, { status });
  });
  transport.prepare([new HumanMessage("fixture scope")], 1);
  const body = { model: start.transport.model, stream: false, max_tokens: 1500,
    tools: MODEL_TOOLS.map((name) => ({ type: "function", function: { name, parameters: { type: "object" } } })),
    messages: [{ role: "user", content: "fixture scope" }] };
  try { return { response: await transport.fetch(`${transport.root.href}/chat/completions`, { method: "POST", body: JSON.stringify(body) }), calls }; }
  catch (error) { return { error, calls }; }
}

describe("private gateway denial diagnostics", () => {
  it.each(codes)("retains only the precise constant %s through framework error causes without retry", async (code) => {
    const result = await deniedTransport(`${code}\n`);
    expect(result.error).toBeInstanceOf(RuntimeFailure);
    expect((result.error as RuntimeFailure).code).toBe(code);
    expect((result.error as Error).message).toBe(code);
    expect(runtimeFailureCode(new Error("framework wrapper", { cause: result.error }))).toBe(code);
    expect(result.calls).toBe(1);
  });

  it.each(codes)("returns blocked %s through the native engine with no Go effects or automatic retry", async (code) => {
    const start = await startFixture();
    let calls = 0; let effects = 0;
    const result = await investigate(start, async () => { effects++; throw new Error("must not execute"); },
      new AbortController().signal, async () => { calls++; return new Response(`${code}\n`, { status: 409 }); });
    expect(result).toMatchObject({ status: "blocked", code, model_requests: 1, tool_steps: 0 });
    expect(calls).toBe(1); expect(effects).toBe(0);
    expect(JSON.stringify(result)).not.toContain("must not execute");
  });

  it.each([
    { content: "CONTEXT_LIMIT\nprivate-provider-material" },
    { content: '{"error":{"message":"USAGE_UNKNOWN private-provider-material"}}' },
    { content: "unrecognized-private-provider-material" },
    { content: "CONTEXT_LIMIT", status: 500 },
  ])("sanitizes unknown or wrong-status responses without trusting embedded error text", async ({ content, status }) => {
    const result = await deniedTransport(content, status);
    expect(result.error).toBeUndefined();
    expect(result.calls).toBe(1);
    expect(await result.response!.json()).toEqual({ error: { message: "Private model gateway rejected this request", type: "gateway_failure" } });
  });
});
