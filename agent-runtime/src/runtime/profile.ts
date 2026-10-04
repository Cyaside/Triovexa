import { registerHarnessProfile } from "deepagents";

export const EXCLUDED_TOOLS = ["ls", "glob", "grep", "write_file", "edit_file", "delete", "execute", "task", "write_todos", "task_status", "task_cancel", "task_result", "task_list"];

export function registerRestrictedProfile(model: string): void {
  // ChatOpenAI reports provider=openai; register for the actual configured identifier.
  if (model.includes(":")) throw new Error("MODEL_IDENTIFIER_UNSUPPORTED");
  registerHarnessProfile(`openai:${model}`, {
    excludedTools: EXCLUDED_TOOLS,
    excludedMiddleware: ["SummarizationMiddleware", "PatchToolCallsMiddleware"],
    generalPurposeSubagent: { enabled: false },
  });
}
