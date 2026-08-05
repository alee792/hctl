import { z } from "zod";

export default {
  description: "Duplicate TypeScript tool.",
  inputSchema: z.object({}).strict(),
  outputSchema: z.object({ ok: z.boolean() }).strict(),
  execute() {
    return { ok: true };
  },
};
