import { isDeepStrictEqual } from "node:util";
import { convertToOpenAITool, isLangChainTool } from "@langchain/core/utils/function_calling";
import { toJsonSchema } from "@langchain/core/utils/json_schema";
import type { ClientTool, ServerTool } from "@langchain/core/tools";
import type { ToolDefinition } from "@langchain/core/language_models/base";
import { RuntimeFailure } from "../bridge/schema.js";

/** Traverse schema positions only; property names and default/enum data survive. */
function withoutDescriptions(schema: unknown): unknown {
  if (typeof schema !== "object" || schema === null || Array.isArray(schema)) return schema;
  const source = schema as Record<string, unknown>;
  const { description: _description, ...result } = source;
  for (const key of ["properties", "patternProperties", "$defs", "definitions", "dependentSchemas"]) {
    const children = source[key];
    if (typeof children === "object" && children !== null && !Array.isArray(children)) result[key] = Object.fromEntries(Object.entries(children).map(([name, child]) => [name, withoutDescriptions(child)]));
  }
  for (const key of ["items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "contains", "propertyNames", "not", "if", "then", "else", "contentSchema"]) {
    if (!(key in source)) continue;
    result[key] = Array.isArray(source[key]) ? source[key].map(withoutDescriptions) : withoutDescriptions(source[key]);
  }
  for (const key of ["allOf", "anyOf", "oneOf", "prefixItems"]) if (Array.isArray(source[key])) result[key] = source[key].map(withoutDescriptions);
  return result;
}

/** Native binding accepts function definitions; execution keeps the original tools. */
export function compactToolDefinition(item: ClientTool | ServerTool): ToolDefinition & ServerTool {
  if (!isLangChainTool(item)) throw new RuntimeFailure("TOOL_INVENTORY_INVALID");
  const native = convertToOpenAITool(item);
  const parameters = toJsonSchema(item.schema, { target: "openapi-3.0" });
  const nativeParameters = native.function.parameters as Record<string, unknown>;
  if (nativeParameters.type !== "object") throw new RuntimeFailure("TOOL_SCHEMA_REPRESENTATION_INVALID");
  const { $schema: _dialect, ...constraints } = nativeParameters;
  // Changing dialect metadata is safe only when every effective constraint is
  // identical. New unsupported schemas fail closed before any model dispatch.
  if (!isDeepStrictEqual(parameters, constraints)) throw new RuntimeFailure("TOOL_SCHEMA_REPRESENTATION_INVALID");
  return { ...native, function: { ...native.function, parameters: withoutDescriptions(parameters) as Record<string, unknown> } };
}
