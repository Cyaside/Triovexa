import type { BackendProtocolV2, ReadResult, ReadRawResult, LsResult, WriteResult, EditResult, DeleteResult, GrepResult, GlobResult } from "deepagents";
import { createHash } from "node:crypto";

// The pinned native read_file tool adds a line-range header to the backend's
// content. Reserve enough for all safe-integer line/offset fields so the final
// tool text, rather than just the unformatted source, stays within this bound.
const READ_HEADER_BYTES = 128;
const READ_OUTPUT_BYTES = 8192;

/** No host filesystem access. Only trusted skills and this attempt's artifacts exist. */
export class VirtualBackend implements BackendProtocolV2 {
  constructor(private readonly state: BackendProtocolV2, private readonly attemptID: string, private readonly signal: AbortSignal) {}

  private allowed(path: string): boolean {
    if (this.signal.aborted || path.includes("\\") || path.includes("\0") || path.split("/").includes("..")) return false;
    return /^\/skills\/[a-z-]+\/SKILL\.md$/.test(path) || new RegExp(`^/artifacts/${encodeURIComponent(this.attemptID).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/[a-f0-9]{64}$`).test(path);
  }

  async read(path: string, offset = 0, limit = 120): Promise<ReadResult> {
    if (!this.allowed(path) || !Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(limit) || limit < 1 || limit > 200) return { error: "VIRTUAL_READ_DENIED" };
    const result = await this.state.read(path, offset, limit);
    if (result.error) return { error: result.error };
    if (typeof result.content !== "string") return { error: "VIRTUAL_RANGE_LIMIT" };
    if (Buffer.byteLength(result.content) <= READ_OUTPUT_BYTES - READ_HEADER_BYTES) return result;

    // Return only complete source lines. The caller can continue at nextOffset;
    // returning an arbitrary character prefix would make the missing remainder
    // of a long line unreachable through the line-based read_file contract.
    const lines = result.content.split("\n");
    let bytes = 0;
    let selected = 0;
    for (const line of lines) {
      const next = Buffer.byteLength(line) + (selected ? 1 : 0);
      if (bytes + next > READ_OUTPUT_BYTES - READ_HEADER_BYTES) break;
      bytes += next;
      selected++;
    }
    if (!selected) return { error: "VIRTUAL_LINE_LIMIT" };
    if (!Number.isSafeInteger(result.startLine) || result.startLine !== offset + 1 ||
      !Number.isSafeInteger(result.endLine) || result.endLine! < result.startLine! ||
      !Number.isSafeInteger(result.totalLines) || result.totalLines! < result.endLine!) return { error: "VIRTUAL_RANGE_LIMIT" };
    const endLine = offset + selected;
    const { nextOffset: _previousOffset, ...window } = result;
    return { ...window, content: lines.slice(0, selected).join("\n"), endLine, ...(endLine < result.totalLines! ? { nextOffset: endLine } : {}) };
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
