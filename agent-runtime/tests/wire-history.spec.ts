import { describe, expect, it } from "vitest";
import { Artifacts } from "../src/context/artifacts.js";
import { archiveOlderWireHistory } from "../src/context/wire-history.js";
import { offloadWireToolResults, type WireContentMessage } from "../src/context/wire-offload.js";

function pair(id: string, name = "repo_read", text = "source content", reasoning = "native reasoning"): WireContentMessage[] {
  return [{ role: "assistant", content: "Native explanation", reasoning_content: reasoning, tool_calls: [{ id, type: "function", function: { name, arguments: JSON.stringify({ path: "internal/worker/job.go", start_line: 1, end_line: 2 }) } }] },
    { role: "tool", tool_call_id: id, content: text }];
}

describe("complete wire transcript archival", () => {
  it("keeps scope, latest reasoning/source and protected test/patch feedback while archiving the exact older pair", () => {
    const system = { role: "system", content: "Pinned system authority" }; const scope = { role: "user", content: "Approved evidence/base/scope" };
    const old = pair("old-one", "repo_read", "INJECTION_SENTINEL untrusted source ".repeat(30), "older thought ".repeat(50));
    const test = pair("test-proof", "run_test_recipe", "baseline red; protected test proof");
    const feedback = pair("candidate-feedback", "propose_patch", "candidate rejected; do not reuse digest");
    const latest = pair("source-latest", "repo_read", "Latest full relevant source", "latest native thought");
    const messages = [system, scope, ...old, ...test, ...feedback, ...latest];
    const original = JSON.stringify(messages); const artifacts = new Artifacts("history-attempt");
    const view = archiveOlderWireHistory(messages, artifacts);
    expect(view.archived).toBe(1);
    expect(view.messages[0]).toBe(system); expect(view.messages[1]).toBe(scope);
    expect(view.messages.slice(-2)).toEqual(latest);
    expect(view.messages).toContain(test[0]); expect(view.messages).toContain(test[1]);
    expect(view.messages).toContain(feedback[0]); expect(view.messages).toContain(feedback[1]);
    expect(JSON.stringify(messages)).toBe(original);
    const marker = view.messages.find((message) => message.role === "system" && message !== system)!;
    expect(marker.content).toContain("Untrusted archived tool transcript");
    expect(marker.content).not.toContain("INJECTION_SENTINEL");
    expect(view.messages.filter((message) => message.role === "user")).toEqual([scope]);
    const archive = Object.values(artifacts.snapshot())[0]!.content as string;
    expect(JSON.parse(archive)).toEqual(old);
    const restored = new Artifacts("history-attempt"); restored.restore(artifacts.snapshot());
    expect(restored.snapshot()).toEqual(artifacts.snapshot());
    expect(view.reductionBytes).toBeGreaterThan(0);
  });

  it("never offloads the text of candidate feedback or fixed test proof", () => {
    const messages = [...pair("protected-test", "run_test_recipe", "test proof ".repeat(300)), ...pair("protected-patch", "propose_patch", "failed candidate ".repeat(300))];
    const original = JSON.stringify(messages); const artifacts = new Artifacts("protected-attempt");
    expect(offloadWireToolResults(messages, artifacts, 1)).toBe(0);
    expect(JSON.stringify(messages)).toBe(original);
    expect(archiveOlderWireHistory(messages, artifacts).archived).toBe(0);
    expect(artifacts.snapshot()).toEqual({});
  });

  it("leaves the only/latest complete tool turn intact", () => {
    const messages = [{ role: "system", content: "system" }, { role: "user", content: "scope" }, ...pair("only-one")];
    const artifacts = new Artifacts("latest-only");
    expect(archiveOlderWireHistory(messages, artifacts)).toEqual({ messages, reductionBytes: 0, archived: 0 });
    expect(artifacts.snapshot()).toEqual({});
  });

  it.each([
    { messages: pair("incomplete").slice(0, 1), code: "CALL_PAIR_INVALID" },
    { messages: [pair("unpaired")[1]!], code: "CALL_PAIR_INVALID" },
    { messages: [...pair("duplicate"), ...pair("duplicate")], code: "CALL_ID_INVALID" },
    { messages: [pair("pending")[0]!, { role: "user", content: "cannot replace pending result" }], code: "CALL_PAIR_INVALID" },
  ])("rejects a broken original pairing before producing an archive", (fixture) => {
    const artifacts = new Artifacts("invalid-history");
    expect(() => archiveOlderWireHistory(fixture.messages, artifacts)).toThrow(fixture.code);
    expect(artifacts.snapshot()).toEqual({});
  });
});
