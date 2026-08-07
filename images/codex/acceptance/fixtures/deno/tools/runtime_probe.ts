import { z } from "zod";

const inputSchema = z.object({}).strict();
const outputSchema = z.object({ runtime: z.literal("deno") }).strict();

export default {
  description: "Report the Deno authored-tool runtime.",
  inputSchema,
  outputSchema,
  execute() {
    return { runtime: "deno" as const };
  },
};
