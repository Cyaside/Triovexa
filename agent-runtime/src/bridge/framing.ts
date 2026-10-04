import { frameSchema, MAX_FRAME_BYTES, RuntimeFailure, type Frame } from "./schema.js";

export function encodeFrame(frame: Frame): Buffer {
  const data = Buffer.from(JSON.stringify(frameSchema.parse(frame)), "utf8");
  if (data.length > MAX_FRAME_BYTES) throw new RuntimeFailure("FRAME_LIMIT");
  const header = Buffer.alloc(4);
  header.writeUInt32BE(data.length);
  return Buffer.concat([header, data]);
}

/** Streaming decoder never buffers a declared oversized body. */
export class FrameDecoder {
  private header = Buffer.alloc(4);
  private headerBytes = 0;
  private body: Buffer | undefined;
  private bodyBytes = 0;
  private failed = false;

  push(chunk: Buffer): Frame[] {
    if (this.failed) throw new RuntimeFailure("PROTOCOL_FAILED");
    const frames: Frame[] = [];
    let offset = 0;
    try {
      while (offset < chunk.length) {
        if (!this.body) {
          const bytes = Math.min(4 - this.headerBytes, chunk.length - offset);
          chunk.copy(this.header, this.headerBytes, offset, offset + bytes);
          this.headerBytes += bytes;
          offset += bytes;
          if (this.headerBytes < 4) continue;
          const length = this.header.readUInt32BE();
          if (length < 2 || length > MAX_FRAME_BYTES) throw new RuntimeFailure("FRAME_LIMIT");
          this.body = Buffer.alloc(length);
          this.bodyBytes = 0;
        }
        const bytes = Math.min(this.body.length - this.bodyBytes, chunk.length - offset);
        chunk.copy(this.body, this.bodyBytes, offset, offset + bytes);
        this.bodyBytes += bytes;
        offset += bytes;
        if (this.bodyBytes !== this.body.length) continue;
        const parsed = frameSchema.safeParse(JSON.parse(this.body.toString("utf8")));
        if (!parsed.success) throw new RuntimeFailure("CONTRACT_INVALID");
        frames.push(parsed.data);
        this.body = undefined;
        this.headerBytes = 0;
      }
    } catch (error) {
      this.failed = true;
      this.body = undefined;
      throw error instanceof RuntimeFailure ? error : new RuntimeFailure("CONTRACT_INVALID");
    }
    return frames;
  }

  end(): void {
    if (this.headerBytes !== 0 || this.body) throw new RuntimeFailure("FRAME_TRUNCATED");
  }
}
