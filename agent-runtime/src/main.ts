import { FrameDecoder, encodeFrame } from "./bridge/framing.js";
import { BridgeSession } from "./bridge/session.js";
import { RuntimeFailure, validateStart } from "./bridge/schema.js";

if (process.versions.node.split(".")[0] !== "24") {
  process.stderr.write("Agent runtime failure: NODE_VERSION_UNSUPPORTED\n");
  process.exit(1);
}

// Set before loading framework packages. Go additionally filters the child's environment.
process.env.LANGSMITH_TRACING = "false";
process.env.LANGCHAIN_TRACING_V2 = "false";
process.env.LANGCHAIN_TRACING = "false";
delete process.env.LANGSMITH_API_KEY;
delete process.env.LANGCHAIN_API_KEY;

const decoder = new FrameDecoder();
const session = new BridgeSession((frame) => { process.stdout.write(encodeFrame(frame)); });
let started = false;
let ending = false;

function protocolFailure(error?: unknown): void {
  session.close();
  const code = error instanceof RuntimeFailure && /^[A-Z_]{1,80}$/.test(error.code) ? error.code : "PROTOCOL_FAILED";
  const fields = code === "CONTRACT_INVALID" && error instanceof RuntimeFailure ? ` (${error.message})` : "";
  process.stderr.write(`Agent runtime failure: ${code}${fields}\n`);
  process.exitCode = 1;
  process.stdin.destroy();
}

process.stdin.on("data", (chunk: Buffer) => {
  try {
    for (const frame of decoder.push(chunk)) {
      const start = session.receive(frame, validateStart);
      if (!start) continue;
      if (started) throw new Error("duplicate start");
      started = true;
      void import("./runtime/engine.js").then(async ({ investigate }) => {
        const deadline = setTimeout(() => session.close(), Math.max(1, Date.parse(start.scope.deadline) - Date.now()));
        try { session.result(await investigate(start, session.call.bind(session), session.abort.signal)); }
        finally { clearTimeout(deadline); ending = true; process.stdin.destroy(); }
      }).catch(protocolFailure);
    }
  } catch (error) { protocolFailure(error); }
});
process.stdin.on("end", () => { if (!ending) { try { decoder.end(); } catch { protocolFailure(); } session.close(); } });
process.stdin.on("error", () => { if (!ending) protocolFailure(); });
process.on("SIGTERM", () => session.close());
process.on("SIGINT", () => session.close());
session.ready();
