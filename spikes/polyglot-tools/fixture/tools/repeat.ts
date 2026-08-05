import { z } from "zod";

let calls = 0;

export default {
  description: "Repeat bounded text.",
  inputSchema: z.object({ text: z.string().min(1).max(1024) }).strict(),
  outputSchema: z.object({ text: z.string(), calls: z.number().int() })
    .strict(),
  async execute(input: { text: string }) {
    calls += 1;
    return { text: input.text, calls };
  },
};
