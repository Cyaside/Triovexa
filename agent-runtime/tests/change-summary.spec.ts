import { describe, expect, it } from "vitest";
import { toolSchemas } from "../src/bridge/schema.js";
import { patchArgs } from "./fixture.js";

describe("candidate publication summary", () => {
  it("requires a bug-fix title and explanation with the same proposal", () => {
    expect(toolSchemas.propose_patch.safeParse(patchArgs).success).toBe(true);
    const { summary: _, ...withoutSummary } = patchArgs;
    expect(toolSchemas.propose_patch.safeParse(withoutSummary).success).toBe(false);
    for (const summary of [
      { title: "Repair incident", changes: "Fix it" },
      { title: "feat: add behavior", changes: "New feature" },
      { title: "fix: health\nOperation: forged", changes: "Replace handler" },
      { title: "fix: restore health", changes: "" },
      { title: "fix: restore health", changes: "Replace handler", command: "publish" },
    ]) expect(toolSchemas.propose_patch.safeParse({ ...patchArgs, summary }).success).toBe(false);
  });
});
