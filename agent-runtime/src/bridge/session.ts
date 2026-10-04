import { CONTRACT_VERSION, ENGINE_VERSION, RuntimeFailure, toolResultSchema, type Frame, type GoToolName, type Start, type ToolResult } from "./schema.js";

type Pending = { id: string; callID: string; resolve: (value: ToolResult) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> };
export type Outcome = { status: "completed" | "blocked" | "failed"; code: string; reason?: string; model_requests: number; tool_steps: number };

export class BridgeSession {
  private incomingOrdinal = 0;
  private outgoingOrdinal = 0;
  private seenFrames = new Set<string>();
  private pending: Pending | undefined;
  private startValue: Start | undefined;
  readonly abort = new AbortController();

  constructor(private readonly send: (frame: Frame) => void, private readonly responseTimeoutMs = 300000) {}

  ready(): void {
    this.send({ contract_version: CONTRACT_VERSION, type: "protocol_ready", id: "ready", case_id: "", attempt_id: "", ordinal: 0,
      payload: { engine_id: "deepagents", engine_version: ENGINE_VERSION, contract_version: 1 } });
  }

  receive(frame: Frame, parseStart: (payload: unknown) => Start): Start | undefined {
    if (frame.ordinal !== this.incomingOrdinal + 1 || this.seenFrames.has(frame.id)) throw new RuntimeFailure("FRAME_ORDER");
    this.incomingOrdinal = frame.ordinal;
    this.seenFrames.add(frame.id);
    if (!this.startValue) {
      if (frame.type !== "start_investigation") throw new RuntimeFailure("FRAME_ORDER");
      const start = parseStart(frame.payload);
      if (frame.case_id !== start.scope.case_id || frame.attempt_id !== start.scope.attempt_id) throw new RuntimeFailure("IDENTITY_MISMATCH");
      this.startValue = start;
      return start;
    }
    if (frame.case_id !== this.startValue.scope.case_id || frame.attempt_id !== this.startValue.scope.attempt_id) throw new RuntimeFailure("IDENTITY_MISMATCH");
    if (frame.type === "cancel") { this.close(new RuntimeFailure("CANCELLED")); return; }
    if (frame.type !== "tool_result" || !this.pending) throw new RuntimeFailure("FRAME_ORDER");
    const result = toolResultSchema.safeParse(frame.payload);
    if (!result.success || result.data.call_id !== this.pending.callID || frame.id !== this.pending.id) throw new RuntimeFailure("CALL_ID_MISMATCH");
    const pending = this.pending;
    this.pending = undefined;
    clearTimeout(pending.timer);
    pending.resolve(result.data);
  }

  async call(callID: string, name: GoToolName, args: Record<string, unknown>): Promise<ToolResult> {
    if (this.abort.signal.aborted) throw new RuntimeFailure("CANCELLED");
    if (this.pending) throw new RuntimeFailure("CONCURRENT_TOOL");
    const id = `tool-${this.outgoingOrdinal + 1}`;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending = undefined; reject(new RuntimeFailure("TOOL_TIMEOUT")); this.abort.abort(); }, this.responseTimeoutMs);
      this.pending = { id, callID, resolve, reject, timer };
      this.emit("tool_request", id, { call_id: callID, name, args });
    });
  }

  result(outcome: Outcome): void { this.emit("investigation_result", `result-${this.outgoingOrdinal + 1}`, outcome); }

  close(error = new RuntimeFailure("PARENT_DISCONNECTED")): void {
    this.abort.abort(error);
    if (this.pending) { clearTimeout(this.pending.timer); this.pending.reject(error); this.pending = undefined; }
  }

  private emit(type: Frame["type"], id: string, payload: Record<string, unknown>): void {
    if (!this.startValue) throw new RuntimeFailure("FRAME_ORDER");
    this.send({ contract_version: CONTRACT_VERSION, type, id, case_id: this.startValue.scope.case_id, attempt_id: this.startValue.scope.attempt_id,
      ordinal: ++this.outgoingOrdinal, payload });
  }
}
