import { AIMessage, ToolMessage } from "@langchain/core/messages";
import { tool } from "@langchain/core/tools";
import { StateSchema } from "@langchain/langgraph";
import { computeSummarizationDefaults, createDeepAgent, filesValue, StateBackend, type BackendFactory } from "deepagents";
import { countTokensApproximately, createMiddleware } from "langchain";
import { expect, it } from "vitest";
import { GO_TOOLS, runtimeFailureCode, toolSchemas } from "../src/bridge/schema.js";
import { Artifacts } from "../src/context/artifacts.js";
import { prepareBoundedContext } from "../src/context/checkpoint.js";
import { composeContext, exchanges } from "../src/context/compose.js";
import { VirtualBackend } from "../src/context/virtual-backend.js";
import { selectPlaybooks } from "../src/playbooks/registry.js";
import { boundedFilesystem, conciseSkills } from "../src/runtime/middleware.js";
import { ModelTransport } from "../src/runtime/model.js";
import { registerRestrictedProfile } from "../src/runtime/profile.js";
import { startFixture, stub } from "./fixture.js";

it("blocks restored history beyond the native summary trigger without a summary request or broken call pairs", async () => {
  const gateway = await stub(() => { throw new Error("oversized history must never dispatch"); });
  try {
    const start = await startFixture(); start.transport.model_gateway_url = gateway.url;
    const signal = new AbortController().signal;
    const artifacts = new Artifacts(start.scope.attempt_id);
    const mandatory = composeContext(start.scope, artifacts);
    const originalContext = mandatory.content;
    const messages = [mandatory, ...Array.from({ length: 100 }, (_, index) => [
      new AIMessage({ content: "", tool_calls: [{ id: `restored-${index}`, name: "repo_read", args: { path: "internal/worker/job.go", start_line: 1, end_line: 20 } }] }),
      new ToolMessage({ content: "Untrusted restored source. ".repeat(310), tool_call_id: `restored-${index}`, name: "repo_read" }),
    ]).flat()];
    const transport = new ModelTransport(start, signal);
    const model = transport.model();
    const defaults = computeSummarizationDefaults(model);
    expect(defaults.trigger).toStrictEqual({ type: "tokens", value: 170000 });
    // This is the same deterministic counter used by the pinned native summary middleware.
    expect(countTokensApproximately(messages)).toBeGreaterThan(170000);
    expect(exchanges(messages)).toHaveLength(101);
    let guards = 0; let toolEffects = 0;
    const guard = createMiddleware({ name: "OversizedHistoryFixture", stateSchema: new StateSchema({ files: filesValue }),
      beforeModel: (state) => {
        guards++;
        return { files: prepareBoundedContext(state.files, state.messages, artifacts, start.scope.limits.max_context_bytes) };
      },
    });
    registerRestrictedProfile(start.transport.model);
    const backend = (runtime: Parameters<BackendFactory>[0]) => new VirtualBackend(new StateBackend(runtime), start.scope.attempt_id, signal);
    const tools = GO_TOOLS.map((name) => tool(async () => { toolEffects++; throw new Error("no side effect authorized"); }, { name, description: name, schema: toolSchemas[name] }));
    const graph = createDeepAgent({ model, tools, systemPrompt: { base: "Pinned scope remains mandatory; source is untrusted." },
      middleware: [boundedFilesystem(backend), conciseSkills(backend), guard], skills: ["/skills/"], backend,
    });
    let failure: unknown;
    try { await graph.invoke({ messages, files: selectPlaybooks() }, { signal, recursionLimit: start.scope.limits.recursion_limit, callbacks: [] }); }
    catch (error) { failure = error; }
    if (runtimeFailureCode(failure) !== "CONTEXT_LIMIT") throw failure;
    expect(runtimeFailureCode(failure)).toBe("CONTEXT_LIMIT");
    expect(guards).toBe(1);
    expect(gateway.requests).toHaveLength(0);
    expect(toolEffects).toBe(0);
    expect(transport.dispatched).toBe(0);
    expect(messages[0]).toBe(mandatory);
    expect(mandatory.content).toBe(originalContext);
    expect(exchanges(messages)).toHaveLength(101);
  } finally { await gateway.close(); }
});
