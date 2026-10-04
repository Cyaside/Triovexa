import { MODEL_TOOLS, type Scope } from "../bridge/schema.js";

const FINAL_SMOKE_TOOLS = ["repo_read", "propose_patch", "cannot_determine", "read_file"] as const;

/** A one-case validation uses known source paths and Go-owned regression tests. */
export function writerToolsForProfile(profile: Scope["profile"]): readonly (typeof MODEL_TOOLS[number])[] {
  return profile === "final-smoke" ? [...FINAL_SMOKE_TOOLS] : [...MODEL_TOOLS];
}
