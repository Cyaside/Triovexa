import { createServer, type IncomingHttpHeaders } from "node:http";
import { readFile } from "node:fs/promises";
import { startSchema, type Start } from "../src/bridge/schema.js";
import { PLAYBOOK_MANIFEST_DIGEST } from "../src/playbooks/registry.js";

export type Captured = { path: string; headers: IncomingHttpHeaders; body: Record<string, unknown> };
export type ResponseFactory = (request: Captured, index: number) => { status?: number; body: unknown; delay?: number };
export async function startFixture(): Promise<Start> {
  const file = new URL("../../testdata/agent-runtime-contract/start.json", import.meta.url);
  const parsed = JSON.parse(await readFile(file, "utf8"));
  parsed.payload.scope.playbook_manifest_digest = PLAYBOOK_MANIFEST_DIGEST;
  parsed.payload.scope.deadline = new Date(Date.now() + 120000).toISOString();
  return startSchema.parse(parsed.payload);
}

export function completion(model: string, calls: Array<{ name: string; args: unknown; id?: string }>, reasoning?: string): Record<string, unknown> {
  return { id: `chatcmpl-offline-${calls.map((call) => call.id ?? call.name).join("-")}`, object: "chat.completion", created: 1, model,
    choices: [{ index: 0, finish_reason: calls.length ? "tool_calls" : "stop", message: { role: "assistant", content: calls.length ? null : "No tool proposal",
      ...(reasoning ? { reasoning_content: reasoning } : {}), tool_calls: calls.map((call, index) => ({ id: call.id ?? `call-${index}`, type: "function", function: { name: call.name, arguments: JSON.stringify(call.args) } })) } }],
    usage: { prompt_tokens: 20, completion_tokens: 10, total_tokens: 30 } };
}

export async function stub(factory: ResponseFactory) {
  const requests: Captured[] = [];
  const server = createServer((request, response) => {
    void (async () => {
      const chunks: Buffer[] = [];
      for await (const chunk of request) chunks.push(Buffer.from(chunk));
      const captured = { path: request.url ?? "", headers: request.headers, body: JSON.parse(Buffer.concat(chunks).toString("utf8")) as Record<string, unknown> };
      requests.push(captured);
      const reply = factory(captured, requests.length);
      const send = () => { response.writeHead(reply.status ?? 200, { "content-type": "application/json" }); response.end(JSON.stringify(reply.body)); };
      if (reply.delay) setTimeout(send, reply.delay); else send();
    })().catch(() => { response.writeHead(500); response.end("fixture failure"); });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("fixture socket missing");
  return { requests, url: `http://127.0.0.1:${address.port}/v1`, close: async () => { server.closeAllConnections(); await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve())); } };
}

export const patchArgs = { patch: "--- a/internal/worker/job.go\n+++ b/internal/worker/job.go\n@@ -1 +1 @@\n-old\n+fixed\n", hypothesis: "The valid job path rejects supported input", evidence_ids: ["log-1"] };
