import { isDeepStrictEqual } from "node:util";
import { convertToOpenAITool, isLangChainTool } from "@langchain/core/utils/function_calling";
import { toJsonSchema } from "@langchain/core/utils/json_schema";
import type { ClientTool, ServerTool } from "@langchain/core/tools";
import type { ToolDefinition } from "@langchain/core/language_models/base";
import { RuntimeFailure } from "../bridge/schema.js";

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
  return { ...native, function: { ...native.function, parameters } };
}
