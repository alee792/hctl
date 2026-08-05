import { z } from "zod";

export default {
  description: "Terminate its language host.",
  inputSchema: z.object({}).strict(),
  outputSchema: z.object({ completed: z.boolean() }).strict(),
  execute() {
    Deno.exit(7);
  },
};
