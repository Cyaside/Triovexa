import { PLAYBOOKS } from "../playbooks/registry.js";
import type { WireContentMessage } from "./wire-offload.js";

/** Verify the exact native full-book window and its paired original read call. */
export function trustedReadReferences(messages: WireContentMessage[]): Map<string, { text: string; reference: string }> {
  const references = new Map<string, { text: string; reference: string }>();
  for (const message of messages) {
    if (message.role !== "assistant") continue;
    for (const call of message.tool_calls ?? []) {
      if (typeof call.id !== "string" || call.function?.name !== "read_file" || typeof call.function.arguments !== "string") continue;
      let args: unknown;
      try { args = JSON.parse(call.function.arguments); } catch { continue; }
      if (typeof args !== "object" || args === null || !("file_path" in args)) continue;
      const book = PLAYBOOKS.find((item) => item.path === args.file_path);
      if (!book || ("offset" in args && args.offset !== 0)) continue;
      const lines = book.content.split("\n");
      if (lines.at(-1) === "") lines.pop();
      const limit = "limit" in args ? args.limit : 120;
      if (typeof limit !== "number" || !Number.isSafeInteger(limit) || limit < lines.length || limit > 200) continue;
      const text = `@@ lines 1-${lines.length} of ${lines.length} @@\n${lines.join("\n")}`;
      const instructionLine = lines.findIndex((line, index) => index > 5 && line.trim() !== "");
      if (instructionLine < 0) continue;
      references.set(call.id, { text, reference: `${book.path}#L${instructionLine + 1}` });
    }
  }
  return references;
}
