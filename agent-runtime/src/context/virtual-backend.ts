import type { BackendProtocolV2, ReadResult, ReadRawResult, LsResult, WriteResult, EditResult, DeleteResult, GrepResult, GlobResult } from "deepagents";
import { createHash } from "node:crypto";

/** No host filesystem access. Only trusted skills and this attempt's artifacts exist. */
export class VirtualBackend implements BackendProtocolV2 {
  constructor(private readonly state: BackendProtocolV2, private readonly attemptID: string, private readonly signal: AbortSignal) {}

  private allowed(path: string): boolean {
    if (this.signal.aborted || path.includes("\\") || path.includes("\0") || path.split("/").includes("..")) return false;
    return /^\/skills\/[a-z-]+\/SKILL\.md$/.test(path) || new RegExp(`^/artifacts/${encodeURIComponent(this.attemptID).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/[a-f0-9]{64}$`).test(path);
  }

  async read(path: string, offset = 0, limit = 120): Promise<ReadResult> {
    if (!this.allowed(path) || !Number.isInteger(offset) || offset < 0 || !Number.isInteger(limit) || limit < 1 || limit > 200) return { error: "VIRTUAL_READ_DENIED" };
    const result = await this.state.read(path, offset, limit);
    if (typeof result.content !== "string" || Buffer.byteLength(result.content) > 8192) return { error: result.error ?? "VIRTUAL_RANGE_LIMIT" };
    return result;
  }

  async readRaw(path: string): Promise<ReadRawResult> {
    if (!this.allowed(path)) return { error: "VIRTUAL_READ_DENIED" };
    return this.state.readRaw(path);
  }

  async ls(path: string): Promise<LsResult> {
    if (this.signal.aborted || !/^\/skills(?:\/[a-z-]+)?\/?$/.test(path)) return { error: "VIRTUAL_LIST_DENIED" };
    return this.state.ls(path);
  }

  // Only trusted framework offload can reach write; the model's write tools are excluded.
  async write(path: string, content: string): Promise<WriteResult> {
    if (!this.allowed(path) || !path.startsWith("/artifacts/") || Buffer.byteLength(content) > 200 * 1024 || path.split("/").at(-1) !== createHash("sha256").update(content).digest("hex")) return { error: "VIRTUAL_WRITE_DENIED" };
    return this.state.write(path, content);
  }
  edit(): EditResult { return { error: "VIRTUAL_WRITE_DENIED" }; }
  delete(): DeleteResult { return { error: "VIRTUAL_WRITE_DENIED" }; }
  grep(): GrepResult { return { error: "VIRTUAL_SEARCH_DENIED" }; }
  glob(): GlobResult { return { error: "VIRTUAL_SEARCH_DENIED" }; }
}
