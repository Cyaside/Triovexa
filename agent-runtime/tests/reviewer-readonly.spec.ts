import { describe, expect, it } from "vitest";
import { invokeReadOnlyReviewer, READ_ONLY_REVIEWER } from "../src/runtime/reviewer.js";
import { startFixture } from "./fixture.js";

describe("read-only reviewer authorization", () => {
  it("is default-off and cannot be activated by a writer profile or invalid delegation depth", async () => {
    expect(READ_ONLY_REVIEWER.enabled).toBe(false);
    expect(READ_ONLY_REVIEWER.tools).toEqual(["read_file"]);
    const start = await startFixture();
    const forbidFetch: typeof fetch = async () => { throw new Error("no network allowed before authorization"); };
    for (const authority of [undefined, { enabled: false }, { enabled: true, stage: "reviewer", depth: 2, reviewer_index: 0, first_request_ordinal: 1 }, { enabled: true, stage: "reviewer", depth: 1, reviewer_index: 1, first_request_ordinal: 1 }]) {
      start.scope.profile = "internal";
      expect(await invokeReadOnlyReviewer(start, authority, {}, new AbortController().signal, forbidFetch)).toMatchObject({ status: "blocked", code: "REVIEWER_DISABLED", model_requests: 0 });
    }
    start.scope.profile = "final-smoke";
    expect(await invokeReadOnlyReviewer(start, { enabled: true, stage: "reviewer", depth: 1, reviewer_index: 0, first_request_ordinal: 1 }, {}, new AbortController().signal, forbidFetch)).toMatchObject({ status: "blocked", code: "REVIEWER_DISABLED", model_requests: 0 });
  });
});
