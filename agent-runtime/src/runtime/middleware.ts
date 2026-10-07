import { createFilesystemMiddleware, createSkillsMiddleware, type BackendFactory } from "deepagents";
import { SystemMessage } from "@langchain/core/messages";
import { RuntimeFailure } from "../bridge/schema.js";
import { PLAYBOOKS, selectPlaybooks } from "../playbooks/registry.js";

/** Retain framework tools/retrieval while replacing generic chat-oriented guidance. */
export function boundedFilesystem(backend: BackendFactory) {
  return createFilesystemMiddleware({ backend, tools: ["read_file"],
    customToolDescriptions: { read_file: "Virtual /skills,/artifacts; #Ln=offset n-1,limit1; repo_read for repo." } });
}

export function conciseSkills(backend: BackendFactory) {
  const native = createSkillsMiddleware({ backend, sources: ["/skills/"] });
  const approved = new Map(PLAYBOOKS.filter((book) => book.path in selectPlaybooks()).map((book) => [book.path, book.id]));
  const concise: typeof native = { ...native,
    // Native beforeModel still loads and checkpoints the trusted skill catalog.
    wrapModelCall: ((request, handler) => {
      const catalog = request.state.skillsMetadata;
      if (!catalog || catalog.length !== approved.size || new Set(catalog.map((entry) => entry.path)).size !== approved.size) throw new RuntimeFailure("PLAYBOOK_VERSION_UNAVAILABLE");
      for (const item of catalog) if (approved.get(item.path) !== item.name) throw new RuntimeFailure("PLAYBOOK_VERSION_UNAVAILABLE");
      const section = `read_file trusted /skills/{name}/SKILL.md: ${catalog.map((item) => item.name).join(",")}.`;
      const content = request.systemMessage.content;
      if (typeof content !== "string" && content.some((part) => part.type !== "text" || typeof part.text !== "string")) throw new RuntimeFailure("CONTRACT_INVALID");
      const text = typeof content === "string" ? content : content.map((part) => part.text).join("\n");
      return handler({ ...request, systemMessage: new SystemMessage(`${text}\n${section}`) });
    }),
  };
  return concise;
}
