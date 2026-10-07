import { z } from "zod";

export const CONTRACT_VERSION = "1";
export const ENGINE_VERSION = "1.14.1";
export const PROMPT_VERSION = "repair-native-v3";
export const MAX_FRAME_BYTES = 256 * 1024;
export const INLINE_RESULT_BYTES = 8 * 1024;
const identifier = z.string().min(1).max(200);
const jsonObject = z.record(z.string(), z.unknown());

export const frameSchema = z.strictObject({
  contract_version: z.literal(CONTRACT_VERSION),
  type: z.enum(["protocol_ready", "start_investigation", "tool_request", "tool_result", "progress", "investigation_result", "cancel"]),
  id: z.string().min(1).max(200),
  case_id: z.string().max(200),
  attempt_id: z.string().max(200),
  ordinal: z.number().int().nonnegative(),
  payload: jsonObject,
});
export type Frame = z.infer<typeof frameSchema>;

export const startSchema = z.strictObject({
  scope: z.strictObject({
    case_id: identifier,
    attempt_id: identifier,
    engine_id: z.literal("deepagents"),
    engine_version: z.literal(ENGINE_VERSION),
    contract_version: z.literal(1),
    checkpoint_thread: identifier,
    provider: z.literal("openai-compatible"),
    provider_config_version: identifier,
    prompt_version: identifier,
    playbook_manifest_digest: identifier,
    base_sha: z.string().regex(/^[a-f0-9]{40,64}$/),
    deployed_sha: z.string().regex(/^[a-f0-9]{40,64}$/),
    allowed_paths: z.array(z.string().min(1).max(512)).min(1).max(20),
    recipe_ids: z.array(identifier).min(1).max(20),
    profile: z.enum(["offline-fixture", "final-smoke", "internal"]),
    deadline: z.iso.datetime({ offset: true }),
    evidence: jsonObject,
    baseline: z.strictObject({ exit_code: z.number().int(), output: z.string().max(32768), duration_ns: z.number().int().nonnegative() }),
    limits: z.strictObject({
      max_model_requests: z.number().int().min(1).max(20),
      max_tool_steps: z.number().int().min(1).max(100),
      max_candidate_count: z.number().int().min(1).max(3),
      max_input_bytes: z.number().int().min(512).max(MAX_FRAME_BYTES),
      max_context_bytes: z.number().int().min(512).max(200 * 1024),
      max_output_tokens: z.number().int().min(1).max(8192),
      recursion_limit: z.number().int().min(2).max(200),
    }),
  }),
  transport: z.strictObject({
    model: identifier,
    model_gateway_url: z.url(),
    capability: z.string().min(1).max(4096),
    input_budget_mode: z.literal("gateway-preview").optional(),
    checkpoint_dsn: z.string().min(1).max(4096).optional(),
    checkpoint_schema: z.string().regex(/^[a-z][a-z0-9_]{0,62}$/).optional(),
  }),
});
export type Start = z.infer<typeof startSchema>;
export type Scope = Start["scope"];

export const toolResultSchema = z.strictObject({
  call_id: identifier,
  status: z.enum(["ok", "error"]),
  value: z.unknown().optional(),
  code: z.string().max(100).optional(),
  message: z.string().max(2048).optional(),
});
export type ToolResult = z.infer<typeof toolResultSchema>;

const scopedPath = z.string().min(1).max(512);
const page = { cursor: z.number().int().nonnegative().optional(), limit: z.number().int().min(1).max(20).optional() };
export const toolSchemas = {
  repo_list: z.strictObject({ prefix: z.string().max(512), ...page }),
  repo_read: z.strictObject({ path: scopedPath, start_line: z.number().int().min(1), end_line: z.number().int().min(1), expected_digest: z.string().max(128).optional() }),
  repo_search: z.strictObject({ prefix: z.string().max(512), query: z.string().min(1).max(256), ...page }),
  run_test_recipe: z.strictObject({ recipe_id: identifier, candidate_digest: z.string().max(128).optional() }),
  propose_patch: z.strictObject({ patch: z.string().min(1).max(65536), hypothesis: z.string().min(1).max(2048), evidence_ids: z.array(identifier).min(1).max(20) }),
  cannot_determine: z.strictObject({ reason: z.string().min(1).max(2048) }),
};
export type GoToolName = keyof typeof toolSchemas;
export const GO_TOOLS = Object.keys(toolSchemas) as GoToolName[];
export const MODEL_TOOLS = [...GO_TOOLS, "read_file"] as const;

export class RuntimeFailure extends Error {
  constructor(readonly code: string, message: string = code) { super(message); this.name = "RuntimeFailure"; }
}

/** Framework wrappers retain causes; inspect a bounded chain without echoing error text. */
export function runtimeFailureCode(error: unknown, unknownCode = "PROVIDER_FAILURE"): string {
  const seen = new Set<Error>();
  for (let depth = 0; depth < 32 && error instanceof Error && !seen.has(error); depth++) {
    if (error instanceof RuntimeFailure) return error.code;
    seen.add(error); error = error.cause;
  }
  return unknownCode;
}

export function validateStart(value: unknown): Start {
  const result = startSchema.safeParse(value);
  if (!result.success) throw new RuntimeFailure("CONTRACT_INVALID", `Invalid contract fields: ${result.error.issues.map((issue) => issue.path.join(".")).join(", ")}`);
  const start = result.data;
  if (start.scope.prompt_version !== PROMPT_VERSION) throw new RuntimeFailure("PROMPT_VERSION_UNAVAILABLE");
  if (start.scope.checkpoint_thread !== `${start.scope.case_id}:${start.scope.attempt_id}`) throw new RuntimeFailure("CHECKPOINT_INCOMPATIBLE");
  if (Date.parse(start.scope.deadline) <= Date.now()) throw new RuntimeFailure("DEADLINE_EXCEEDED");
  if (start.scope.profile === "final-smoke" && (start.scope.limits.max_candidate_count !== 1 || start.scope.limits.max_model_requests > 4 || start.scope.limits.max_output_tokens > 1500)) throw new RuntimeFailure("CONTRACT_INVALID");
  if (start.scope.profile !== "offline-fixture" && (!start.transport.checkpoint_dsn || !start.transport.checkpoint_schema)) throw new RuntimeFailure("CHECKPOINT_REQUIRED");
  return start;
}
