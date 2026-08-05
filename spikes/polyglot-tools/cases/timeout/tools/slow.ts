import { z } from "zod";

export default {
  description: "Sleep until the supervisor terminates the host.",
  inputSchema: z.object({}).strict(),
  outputSchema: z.object({ completed: z.boolean() }).strict(),
  async execute() {
    await new Promise((resolve) => setTimeout(resolve, 60_000));
    return { completed: true };
  },
};
