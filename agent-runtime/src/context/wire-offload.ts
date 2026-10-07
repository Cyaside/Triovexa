import { Artifacts } from "./artifacts.js";
import { trustedReadReferences } from "./trusted-read.js";

type TextBlock = { type: string; text?: unknown };
export type WireContentMessage = { role?: string; content?: unknown; tool_call_id?: string; reasoning_content?: string;
  tool_calls?: Array<{ id: string; type: string; function: { name: string; arguments: string } }> };

/**
 * Change only tool-result text, never assistant thoughts/calls or user scope.
 * Native read_file returns text blocks; keep their shape and replace only text.
 * The original bytes remain in a content-addressed, same-attempt virtual file.
 */
export function offloadWireToolResults(messages: WireContentMessage[], artifacts: Artifacts, minimumReductionBytes = Infinity): number {
  let replacements = 0;
  let reducedBytes = 0;
  const trusted = trustedReadReferences(messages);
  const protectedResults = new Set(messages.filter((message) => message.role === "assistant").flatMap((message) => message.tool_calls ?? [])
    .filter((call) => ["propose_patch", "run_test_recipe", "cannot_determine"].includes(call.function.name)).map((call) => call.id));
  const candidates: Array<{ text: string; trustedReference?: string; replace: (reference: string) => void }> = [];
  for (const message of messages) {
    if (message.role !== "tool") continue;
    if (message.tool_call_id && protectedResults.has(message.tool_call_id)) continue;
    const knownRead = message.tool_call_id ? trusted.get(message.tool_call_id) : undefined;
    if (typeof message.content === "string") {
      candidates.push({ text: message.content, ...(message.content === knownRead?.text ? { trustedReference: knownRead.reference } : {}), replace: (reference) => { message.content = reference; } });
    } else if (Array.isArray(message.content)) {
      for (const value of message.content) {
        if (typeof value !== "object" || value === null) continue;
        const block = value as TextBlock;
        if (block.type !== "text" || typeof block.text !== "string") continue;
        candidates.push({ text: block.text, ...(block.text === knownRead?.text ? { trustedReference: knownRead.reference } : {}), replace: (reference) => { block.text = reference; } });
      }
    }
  }
  // Start with the largest outputs so a newly requested small source window can
  // remain readable. The gateway remeasures this view; byte savings are only a
  // selection heuristic and never permission to bypass its authoritative cap.
  candidates.sort((left, right) => Buffer.byteLength(right.text) - Buffer.byteLength(left.text));
  for (const candidate of candidates) {
    if (reducedBytes >= minimumReductionBytes) break;
    const reference = candidate.trustedReference ?? artifacts.offloadText(candidate.text);
    if (reference === undefined) continue;
    if (candidate.trustedReference) artifacts.put(candidate.text);
    candidate.replace(reference); replacements++;
    reducedBytes += Buffer.byteLength(candidate.text) - Buffer.byteLength(reference);
  }
  return replacements;
}
