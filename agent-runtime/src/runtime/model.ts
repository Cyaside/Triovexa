import { ChatOpenAI } from "@langchain/openai";
import { type BaseMessage, isAIMessage } from "@langchain/core/messages";
import { z } from "zod";
import { RuntimeFailure, MAX_FRAME_BYTES, toolSchemas, type Start, type GoToolName } from "../bridge/schema.js";
import { writerToolsForProfile } from "./tool-inventory.js";
import { Artifacts } from "../context/artifacts.js";
import { offloadWireToolResults, type WireContentMessage } from "../context/wire-offload.js";
import { archiveOlderWireHistory } from "../context/wire-history.js";

const virtualRead = z.strictObject({ file_path: z.string().max(512), offset: z.number().int().nonnegative().optional(), limit: z.number().int().min(1).max(200).optional() });
type WireMessage = WireContentMessage & { tool_calls?: Array<{ id: string; type: string; function: { name: string; arguments: string } }>; reasoning_content?: string };

// These exact constants are emitted only by the private Go admission endpoint.
// Never accept a prefix, JSON error field or arbitrary provider error as a code.
const gatewayDenialCodes = new Set([
  "CONTEXT_LIMIT", "BUDGET_EXHAUSTED", "PROVIDER_DISPATCH_UNCERTAIN", "USAGE_UNKNOWN",
  "OFFLINE_EGRESS_DENIED", "PRICING_UNKNOWN", "BILLING_UNBOUNDED", "MODEL_DISPATCH_BLOCKED",
]);

export function isGatewayDenialCode(code: string): boolean { return gatewayDenialCodes.has(code); }

export function gatewayRoot(raw: string): URL {
  const url = new URL(raw);
  if (url.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(url.hostname) || url.username || url.password || url.search || url.hash || !["", "/", "/v1", "/v1/"].includes(url.pathname)) throw new RuntimeFailure("GATEWAY_DENIED");
  url.pathname = "/v1";
  return url;
}

function validateNativeResponse(value: unknown, model: string, allowedTools: readonly string[]): void {
  if (typeof value !== "object" || value === null) throw new RuntimeFailure("PROVIDER_CONTRACT_INVALID");
  const response = value as { model?: string; choices?: Array<{ message?: WireMessage }> };
  if (response.model !== model || !Array.isArray(response.choices) || response.choices.length !== 1 || response.choices[0]?.message?.role !== "assistant") throw new RuntimeFailure("PROVIDER_CONTRACT_INVALID");
  const calls = response.choices[0].message.tool_calls ?? [];
  if (!Array.isArray(calls) || calls.length > 10) throw new RuntimeFailure("PROVIDER_CONTRACT_INVALID");
  const ids = new Set<string>();
  for (const call of calls) {
    if (typeof call.id !== "string" || !call.id || call.id.length > 128 || ids.has(call.id) || call.type !== "function" || !allowedTools.includes(call.function?.name)) throw new RuntimeFailure("CALL_ID_INVALID");
    ids.add(call.id);
    const schema = call.function.name === "read_file" ? virtualRead : toolSchemas[call.function.name as GoToolName];
    let args: unknown;
    try { args = JSON.parse(call.function.arguments); } catch { throw new RuntimeFailure("TOOL_ARGUMENT_INVALID"); }
    if (!schema.safeParse(args).success) throw new RuntimeFailure("TOOL_ARGUMENT_INVALID");
  }
  if (calls.length !== 1 && calls.some((call) => ["propose_patch", "cannot_determine"].includes(call.function.name))) throw new RuntimeFailure("TERMINAL_CALL_MIXED");
}

async function boundedResponse(response: Response): Promise<string> {
  if (!response.body) throw new RuntimeFailure("PROVIDER_CONTRACT_INVALID");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  try {
    while (true) {
      const result = await reader.read();
      if (result.done) break;
      bytes += result.value.byteLength;
      if (bytes > MAX_FRAME_BYTES) { await reader.cancel(); throw new RuntimeFailure("PROVIDER_RESPONSE_LIMIT"); }
      chunks.push(result.value);
    }
  } finally { reader.releaseLock(); }
  return Buffer.concat(chunks).toString("utf8");
}

/** Preserves the one provider extension the wire contract explicitly supports. */
export class ModelTransport {
  private ordinal = 0;
  private reasoning = new Map<string, string>();
  dispatched = 0;
  readonly root: URL;

  constructor(private readonly start: Start, private readonly signal: AbortSignal, private readonly underlyingFetch: typeof fetch = fetch, private readonly inventory: "writer" | "reviewer" = "writer", private readonly artifacts?: Artifacts) {
    this.root = gatewayRoot(start.transport.model_gateway_url);
  }

  prepare(messages: BaseMessage[], ordinal: number): void {
    if (ordinal < 1 || ordinal > this.start.scope.limits.max_model_requests) throw new RuntimeFailure("BUDGET_EXHAUSTED");
    this.ordinal = ordinal;
    this.reasoning.clear();
    for (const message of messages) {
      if (!isAIMessage(message)) continue;
      const content = message.additional_kwargs.reasoning_content;
      if (typeof content === "string") for (const call of message.tool_calls ?? []) if (call.id) this.reasoning.set(call.id, content);
    }
  }

  fetch: typeof fetch = async (input, init) => {
    this.signal.throwIfAborted();
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    if (url.href !== `${this.root.href}/chat/completions` || !this.ordinal || typeof init?.body !== "string" || init.method !== "POST") throw new RuntimeFailure("GATEWAY_DENIED");
    const body = JSON.parse(init.body) as { model: string; stream: boolean; max_tokens?: number; max_completion_tokens?: number; messages: WireMessage[]; tools?: Array<{ function?: { name?: string } }> };
    if (body.model !== this.start.transport.model || body.stream !== false) throw new RuntimeFailure("PROVIDER_CONTRACT_INVALID");
    const allowedTools = this.inventory === "reviewer" ? ["read_file"] : writerToolsForProfile(this.start.scope.profile);
    if (!Array.isArray(body.tools) || JSON.stringify(body.tools.map((item) => item.function?.name).sort()) !== JSON.stringify([...allowedTools].sort())) throw new RuntimeFailure("TOOL_INVENTORY_INVALID");
    for (const message of body.messages) {
      if (message.role !== "assistant" || !message.tool_calls?.length) continue;
      const content = this.reasoning.get(message.tool_calls[0]!.id);
      if (content !== undefined) message.reasoning_content = content;
    }
    let encoded = JSON.stringify(body);
    if ((body.max_tokens ?? body.max_completion_tokens) !== this.start.scope.limits.max_output_tokens) throw new RuntimeFailure("OUTPUT_LIMIT_MISSING");
    const headers = new Headers(init.headers);
    headers.delete("X-Triovexa-Context-Preview");
    headers.set("Authorization", `Bearer ${this.start.transport.capability}`);
    headers.set("X-Triovexa-Case-ID", this.start.scope.case_id);
    headers.set("X-Triovexa-Attempt-ID", this.start.scope.attempt_id);
    headers.set("X-Triovexa-Request-Ordinal", String(this.ordinal));
    const signals = [this.signal, AbortSignal.timeout(Math.min(300000, Math.max(1, Date.parse(this.start.scope.deadline) - Date.now())))];
    if (init.signal) signals.push(init.signal);
    const dispatchSignal = AbortSignal.any(signals);
    if (this.start.transport.input_budget_mode === "gateway-preview") {
      if (!this.artifacts) throw new RuntimeFailure("CONTEXT_CONFIG_INVALID");
      const preview = async (payload: string) => {
        const previewHeaders = new Headers(headers); previewHeaders.set("X-Triovexa-Context-Preview", "1");
        const response = await this.underlyingFetch(url, { ...init, body: payload, headers: previewHeaders, redirect: "error", signal: dispatchSignal });
        const content = await boundedResponse(response);
        if (response.status === 409 && isGatewayDenialCode(content.trim())) throw new RuntimeFailure(content.trim());
        let value: unknown;
        try { value = JSON.parse(content); } catch { throw new RuntimeFailure("CONTEXT_PREVIEW_INVALID"); }
        const contract = z.strictObject({ input_bound: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER), max_input_tokens: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER) }).safeParse(value);
        if (!response.ok || !contract.success) throw new RuntimeFailure("CONTEXT_PREVIEW_INVALID");
        return contract.data;
      };
      const measured = await preview(encoded);
      if (measured.input_bound > measured.max_input_tokens || Buffer.byteLength(encoded) > this.start.scope.limits.max_input_bytes) {
        const excess = Math.max(measured.input_bound - measured.max_input_tokens, Buffer.byteLength(encoded) - this.start.scope.limits.max_input_bytes);
        const history = archiveOlderWireHistory(body.messages, this.artifacts);
        body.messages = history.messages;
        const remaining = Math.max(0, excess - history.reductionBytes);
        const replacements = remaining ? offloadWireToolResults(body.messages, this.artifacts, remaining) : 0;
        if (!history.archived && !replacements) throw new RuntimeFailure("CONTEXT_LIMIT");
        encoded = JSON.stringify(body);
        const reduced = await preview(encoded);
        if (reduced.input_bound > reduced.max_input_tokens) throw new RuntimeFailure("CONTEXT_LIMIT");
      }
    }
    if (Buffer.byteLength(encoded) > this.start.scope.limits.max_input_bytes) throw new RuntimeFailure("CONTEXT_LIMIT");
    this.dispatched++;
    const response = await this.underlyingFetch(url, { ...init, body: encoded, headers, redirect: "error", signal: dispatchSignal });
    const content = await boundedResponse(response);
    if (response.status === 409 && isGatewayDenialCode(content.trim())) throw new RuntimeFailure(content.trim());
    if (response.ok) {
      let value: unknown;
      try { value = JSON.parse(content); } catch { throw new RuntimeFailure("PROVIDER_CONTRACT_INVALID"); }
      validateNativeResponse(value, this.start.transport.model, allowedTools);
    }
    // Failed responses are already accounted by Go; never echo a raw provider error.
    return new Response(response.ok ? content : JSON.stringify({ error: { message: "Private model gateway rejected this request", type: "gateway_failure" } }), { status: response.status, headers: { "content-type": "application/json" } });
  };

  model(): ChatOpenAI {
    return new ChatOpenAI({
      model: this.start.transport.model, apiKey: "private-gateway", useResponsesApi: false,
      streaming: false, streamUsage: false, maxRetries: 0, timeout: 300000,
      maxTokens: this.start.scope.limits.max_output_tokens,
      modelKwargs: { parallel_tool_calls: false },
      configuration: { baseURL: this.root.href, fetch: this.fetch, maxRetries: 0 },
    });
  }
}
