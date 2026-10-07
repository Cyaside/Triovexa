import { RuntimeFailure } from "../bridge/schema.js";
import { Artifacts, transcriptReference } from "./artifacts.js";
import type { WireContentMessage } from "./wire-offload.js";

function textBytes(content: unknown): number {
  if (typeof content === "string") return Buffer.byteLength(content);
  if (!Array.isArray(content)) return 0;
  return content.reduce((total, block) => total + (typeof block === "object" && block !== null && block.type === "text" && typeof block.text === "string" ? Buffer.byteLength(block.text) : 0), 0);
}

/** Archive complete older turns; keep scope and the latest paired turn intact. */
export function archiveOlderWireHistory(messages: WireContentMessage[], artifacts: Artifacts): { messages: WireContentMessage[]; reductionBytes: number; archived: number } {
  const groups: WireContentMessage[][] = [];
  const pending = new Set<string>(); const seen = new Set<string>();
  for (const message of messages) {
    if (message.role === "assistant" && message.tool_calls?.length) {
      if (pending.size) throw new RuntimeFailure("CALL_PAIR_INVALID");
      for (const call of message.tool_calls) {
        if (!call.id || seen.has(call.id)) throw new RuntimeFailure("CALL_ID_INVALID");
        pending.add(call.id); seen.add(call.id);
      }
      groups.push([message]);
    } else if (message.role === "tool") {
      if (!message.tool_call_id || !pending.delete(message.tool_call_id)) throw new RuntimeFailure("CALL_PAIR_INVALID");
      groups.at(-1)!.push(message);
    } else {
      if (pending.size) throw new RuntimeFailure("CALL_PAIR_INVALID");
      groups.push([message]);
    }
  }
  if (pending.size) throw new RuntimeFailure("CALL_PAIR_INVALID");
  const paired = groups.filter((group) => group[0]?.role === "assistant" && group[0].tool_calls?.length);
  const latest = paired.at(-1);
  const archived = groups.filter((group) => group !== latest && group[0]?.role === "assistant" && group[0].tool_calls?.length &&
    !group[0].tool_calls.some((call) => ["propose_patch", "run_test_recipe", "cannot_determine"].includes(call.function.name)));
  if (!archived.length) return { messages, reductionBytes: 0, archived: 0 };
  // Original thoughts, visible content, call arguments and results remain in
  // both the durable graph transcript and this immutable, paired wire archive.
  const path = artifacts.put(JSON.stringify(archived.flat(), null, 2));
  const reference = transcriptReference(path);
  const marker: WireContentMessage = { role: "system", content: reference };
  let inserted = false;
  const retained: WireContentMessage[] = [];
  for (const group of groups) {
    if (!archived.includes(group)) { retained.push(...group); continue; }
    if (!inserted) { retained.push(marker); inserted = true; }
  }
  // This excludes removed role/function-template overhead and is deliberately
  // only a conservative text-size heuristic. Go remeasures the complete wire
  // payload before it can reserve or dispatch any model request.
  const removedText = archived.flat().reduce((bytes, message) => bytes + textBytes(message.content) + (typeof message.reasoning_content === "string" ? Buffer.byteLength(message.reasoning_content) : 0), 0);
  return { messages: retained, reductionBytes: Math.max(0, removedText - Buffer.byteLength(reference) - 32), archived: archived.length };
}
