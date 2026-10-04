import type { BaseMessage } from "@langchain/core/messages";
import type { FileData } from "deepagents";
import { RuntimeFailure } from "../bridge/schema.js";
import { selectPlaybooks } from "../playbooks/registry.js";
import { Artifacts } from "./artifacts.js";
import { compactContext } from "./compose.js";

export function restoreTrustedFiles(files: unknown, artifacts: Artifacts, trusted = selectPlaybooks()): void {
  if (typeof files !== "object" || files === null || Array.isArray(files)) throw new RuntimeFailure("CHECKPOINT_INCOMPATIBLE");
  const entries = files as Record<string, unknown>;
  for (const [path, entry] of Object.entries(entries)) {
    if (!path.startsWith("/skills/")) continue;
    if (!(path in trusted) || typeof entry !== "object" || entry === null || !("content" in entry) || entry.content !== (trusted[path] as { content: string }).content) throw new RuntimeFailure("CHECKPOINT_INCOMPATIBLE");
  }
  for (const path of Object.keys(trusted)) if (!(path in entries)) throw new RuntimeFailure("CHECKPOINT_INCOMPATIBLE");
  artifacts.restore(files);
}

/** Both graph startup and restored state use the same guard before model dispatch. */
export function prepareBoundedContext(files: unknown, messages: BaseMessage[], artifacts: Artifacts, maxBytes: number, trusted = selectPlaybooks()): Record<string, FileData> {
  restoreTrustedFiles(files, artifacts, trusted);
  compactContext(messages, maxBytes, artifacts);
  return artifacts.snapshot();
}
