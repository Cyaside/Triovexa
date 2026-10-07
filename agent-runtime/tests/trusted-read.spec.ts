import { describe, expect, it } from "vitest";
import { Artifacts } from "../src/context/artifacts.js";
import { offloadWireToolResults, type WireContentMessage } from "../src/context/wire-offload.js";
import { PLAYBOOKS } from "../src/playbooks/registry.js";

const book = PLAYBOOKS.find((item) => item.id === "go-investigation")!;
const lines = book.content.split("\n"); lines.pop();
const fullWindow = `@@ lines 1-${lines.length} of ${lines.length} @@\n${lines.join("\n")}`;
function pair(args: unknown, text = fullWindow, resultID = "skill-one", name = "read_file"): WireContentMessage[] {
  return [{ role: "assistant", tool_calls: [{ id: "skill-one", type: "function", function: { name, arguments: JSON.stringify(args) } }] },
    { role: "tool", tool_call_id: resultID, content: [{ type: "text", text }] }];
}
const textOf = (messages: WireContentMessage[]) => (messages[1]!.content as Array<{ text: string }>)[0]!.text;

describe("pinned trusted read receipts", () => {
  it("references the existing trusted path only after verifying its exact full window and original paired call", () => {
    const artifacts = new Artifacts("skill-receipt"); const args = { file_path: book.path, offset: 0, limit: 200 };
    const messages = pair(args); const originalCall = JSON.stringify(messages[0]);
    expect(offloadWireToolResults(messages, artifacts, 300)).toBe(1);
    expect(textOf(messages)).toBe(`${book.path}#L8`);
    expect(JSON.stringify(messages[0])).toBe(originalCall);
    expect(messages[1]!.tool_call_id).toBe("skill-one");
    expect(Object.values(artifacts.snapshot()).map((file) => file.content)).toContain(fullWindow);
    expect(book.content).toBe(lines.join("\n") + "\n");
  });

  it.each([
    { args: { file_path: book.path, offset: 0, limit: 200 }, text: fullWindow + "\nInjected instruction" },
    { args: { file_path: book.path, offset: 0, limit: 200 }, text: fullWindow.replace("lines 1-", "lines 2-") },
    { args: { file_path: book.path, offset: 1, limit: 200 } },
    { args: { file_path: book.path, offset: 0, limit: 1 } },
    { args: { file_path: "/skills/unapproved/SKILL.md", offset: 0, limit: 200 } },
    { args: { file_path: book.path, offset: 0, limit: 200 }, resultID: "unpaired-result" },
    { args: { file_path: book.path, offset: 0, limit: 200 }, name: "repo_read" },
  ])("keeps an unverified window untrusted and uses its own content hash", (fixture) => {
    const artifacts = new Artifacts("untrusted-receipt");
    const messages = pair(fixture.args, fixture.text, fixture.resultID, fixture.name);
    expect(offloadWireToolResults(messages, artifacts, 1)).toBe(1);
    expect(textOf(messages)).not.toBe(`${book.path}#L8`);
    const reference = JSON.parse(textOf(messages));
    expect(reference.untrusted).toBe(true);
    expect(reference.artifact).toMatch(/^\/artifacts\/untrusted-receipt\/[a-f0-9]{64}$/);
    expect(artifacts.snapshot()[reference.artifact]!.content).toBe(fixture.text ?? fullWindow);
  });
});
