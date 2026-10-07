import { describe, expect, it } from "vitest";
import { tool } from "@langchain/core/tools";
import { convertToOpenAITool } from "@langchain/core/utils/function_calling";
import { z } from "zod";
import { compactToolDefinition } from "../src/runtime/tool-definition.js";
import { GO_TOOLS, toolSchemas } from "../src/bridge/schema.js";
import { boundedFilesystem } from "../src/runtime/middleware.js";
import { StateBackend } from "deepagents";

describe("native tool schema serialization equivalence", () => {
  it("retains every native constraint for the exact seven tools, omitting dialect and parameter descriptions", () => {
    const tools = [...GO_TOOLS.map((name) => tool(async () => "", { name, description: name, schema: toolSchemas[name] })), ...boundedFilesystem((runtime) => new StateBackend(runtime)).tools!];
    expect(tools).toHaveLength(7);
    let savedBytes = 0;
    for (const original of tools) {
      const native = convertToOpenAITool(original);
      const nativeParameters = native.function.parameters as Record<string, unknown>;
      const { $schema, ...constraints } = nativeParameters;
      expect($schema).toBe("https://json-schema.org/draft/2020-12/schema");
      const compact = compactToolDefinition(original);
      let expected = constraints;
      if (native.function.name === "read_file") {
        const properties = { ...(constraints.properties as Record<string, Record<string, unknown>>) };
        for (const name of ["file_path", "offset", "limit"]) {
          const { description: _description, ...validation } = properties[name]!;
          properties[name] = validation;
        }
        expected = { ...constraints, properties };
      }
      expect(compact.function.parameters).toStrictEqual(expected);
      expect(compact.function).toStrictEqual({ ...native.function, parameters: expected });
      expect((compact.function.parameters as Record<string, unknown>).additionalProperties).toBe(false);
      expect(nativeParameters.$schema).toBe($schema); // Original native tool/schema is unchanged.
      savedBytes += Buffer.byteLength(JSON.stringify(native)) - Buffer.byteLength(JSON.stringify(compact));
    }
    expect(savedBytes).toBeGreaterThan(7 * Buffer.byteLength('"$schema":"https://json-schema.org/draft/2020-12/schema",') + 150);
  });

  it("retains required, integer bounds, string/array bounds and enums exactly", () => {
    const original = tool(async () => "", { name: "equivalence-only", description: "fixture", schema: z.strictObject({
      count: z.number().int().min(1).max(20), text: z.string().min(1).max(256), status: z.enum(["open", "closed"]), paths: z.array(z.string().max(512)).min(1).max(20), optional: z.string().optional(),
    }) });
    const native = convertToOpenAITool(original);
    const { $schema: _, ...constraints } = native.function.parameters as Record<string, unknown>;
    expect(compactToolDefinition(original).function.parameters).toStrictEqual(constraints);
  });

  it("preserves a property named description and default data while removing only its schema annotation", async () => {
    const original = tool(async (input) => JSON.stringify(input), { name: "named-description", description: "Required operator guidance", schema: z.strictObject({
      description: z.string().min(3).max(20).describe("Verbose field guidance"),
      state: z.strictObject({ description: z.string().min(1).max(20) }).default({ description: "literal default data" }),
    }) });
    const compact = compactToolDefinition(original);
    const parameters = compact.function.parameters as { properties: { description: unknown; state: unknown }; required: string[]; additionalProperties: boolean };
    expect(parameters.properties.description).toStrictEqual({ type: "string", minLength: 3, maxLength: 20 });
    expect(parameters.properties.state).toMatchObject({ default: { description: "literal default data" }, properties: { description: { type: "string", minLength: 1, maxLength: 20 } }, additionalProperties: false });
    expect(parameters.required).toContain("description");
    expect(parameters.additionalProperties).toBe(false);
    expect(compact.function.description).toBe("Required operator guidance");
    expect(JSON.parse(await original.invoke({ description: "valid" }))).toEqual({ description: "valid", state: { description: "literal default data" } });
    await expect(original.invoke({ description: "no" })).rejects.toThrow();
  });
});
