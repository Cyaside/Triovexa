import { describe, expect, it } from "vitest";
import { tool } from "@langchain/core/tools";
import { convertToOpenAITool } from "@langchain/core/utils/function_calling";
import { z } from "zod";
import { compactToolDefinition } from "../src/runtime/tool-definition.js";
import { GO_TOOLS, toolSchemas } from "../src/bridge/schema.js";
import { boundedFilesystem } from "../src/runtime/middleware.js";
import { StateBackend } from "deepagents";

describe("native tool schema serialization equivalence", () => {
  it("retains every native constraint for the exact seven tools, omitting only dialect metadata", () => {
    const tools = [...GO_TOOLS.map((name) => tool(async () => "", { name, description: name, schema: toolSchemas[name] })), ...boundedFilesystem((runtime) => new StateBackend(runtime)).tools!];
    expect(tools).toHaveLength(7);
    let savedBytes = 0;
    for (const original of tools) {
      const native = convertToOpenAITool(original);
      const nativeParameters = native.function.parameters as Record<string, unknown>;
      const { $schema, ...constraints } = nativeParameters;
      expect($schema).toBe("https://json-schema.org/draft/2020-12/schema");
      const compact = compactToolDefinition(original);
      expect(compact.function.parameters).toStrictEqual(constraints);
      expect(compact.function).toStrictEqual({ ...native.function, parameters: constraints });
      expect((compact.function.parameters as Record<string, unknown>).additionalProperties).toBe(false);
      expect(nativeParameters.$schema).toBe($schema); // Original native tool/schema is unchanged.
      savedBytes += Buffer.byteLength(JSON.stringify(native)) - Buffer.byteLength(JSON.stringify(compact));
    }
    expect(savedBytes).toBe(7 * Buffer.byteLength('"$schema":"https://json-schema.org/draft/2020-12/schema",'));
  });

  it("retains required, integer bounds, string/array bounds and enums exactly", () => {
    const original = tool(async () => "", { name: "equivalence-only", description: "fixture", schema: z.strictObject({
      count: z.number().int().min(1).max(20), text: z.string().min(1).max(256), status: z.enum(["open", "closed"]), paths: z.array(z.string().max(512)).min(1).max(20), optional: z.string().optional(),
    }) });
    const native = convertToOpenAITool(original);
    const { $schema: _, ...constraints } = native.function.parameters as Record<string, unknown>;
    expect(compactToolDefinition(original).function.parameters).toStrictEqual(constraints);
  });
});
