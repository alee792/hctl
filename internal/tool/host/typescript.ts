import { basename, extname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { z } from "zod";

type Tool = {
  description: string;
  inputSchema: z.ZodType;
  outputSchema: z.ZodType;
  execute: (input: unknown, context: { requestId: string }) => unknown;
};

type Request = {
  id: string;
  method: "list" | "call";
  params?: { name?: string; arguments?: unknown };
};

function isStrictObject(schema: z.ZodType | undefined): boolean {
  const definition = (schema as unknown as {
    def?: { type?: string; catchall?: { def?: { type?: string } } };
  })?.def;
  return definition?.type === "object" &&
    definition.catchall?.def?.type === "never";
}

const root = Deno.args[0];
if (!root) throw new Error("project root is required");

const tools = new Map<string, Tool>();
let startupError = "";
try {
  const entries = [...Deno.readDirSync(join(root, "tools"))].sort((
    left,
    right,
  ) => left.name.localeCompare(right.name));
  for (const entry of entries) {
    if (
      !entry.isFile || extname(entry.name) !== ".ts" ||
      entry.name.startsWith("_")
    ) continue;
    const name = basename(entry.name, ".ts").replaceAll("_", "-");
    const module = await import(
      pathToFileURL(join(root, "tools", entry.name)).href
    );
    const candidate = module.default as Partial<Tool>;
    if (
      typeof candidate?.description !== "string" ||
      !(candidate.inputSchema instanceof z.ZodType) ||
      !isStrictObject(candidate.inputSchema) ||
      !(candidate.outputSchema instanceof z.ZodType) ||
      !isStrictObject(candidate.outputSchema) ||
      typeof candidate.execute !== "function"
    ) {
      startupError =
        `${entry.name} must default-export a description, strict Zod object inputSchema and outputSchema, and execute function`;
      tools.clear();
      break;
    }
    tools.set(name, candidate as Tool);
  }
} catch {
  startupError = "TypeScript tool modules could not be loaded";
}

const instanceId = `typescript:${Deno.pid}`;
const encoder = new TextEncoder();

function reply(value: unknown) {
  Deno.stdout.writeSync(encoder.encode(`${JSON.stringify(value)}\n`));
}

async function dispatch(request: Request) {
  if (startupError) throw new Error(startupError);
  if (request.method === "list") {
    return {
      instanceId,
      tools: [...tools.entries()].map(([name, tool]) => ({
        name,
        description: tool.description,
        inputSchema: z.toJSONSchema(tool.inputSchema),
        outputSchema: z.toJSONSchema(tool.outputSchema),
      })),
    };
  }
  const name = request.params?.name ?? "";
  const tool = tools.get(name);
  if (!tool) throw new Error(`unknown TypeScript tool ${JSON.stringify(name)}`);
  const input = tool.inputSchema.parse(request.params?.arguments);
  const result = await tool.execute(input, { requestId: request.id });
  return { instanceId, output: tool.outputSchema.parse(result) };
}

let pending = "";
for await (
  const chunk of Deno.stdin.readable.pipeThrough(new TextDecoderStream())
) {
  pending += chunk;
  for (;;) {
    const newline = pending.indexOf("\n");
    if (newline < 0) {
      if (encoder.encode(pending).byteLength > 64 * 1024) {
        throw new Error("protocol line exceeds 64 KiB");
      }
      break;
    }
    if (encoder.encode(pending.slice(0, newline)).byteLength > 64 * 1024) {
      throw new Error("protocol line exceeds 64 KiB");
    }
    const line = pending.slice(0, newline);
    pending = pending.slice(newline + 1);
    if (!line) continue;
    let request: Request | undefined;
    try {
      request = JSON.parse(line) as Request;
      reply({ id: request.id, result: await dispatch(request) });
    } catch (error) {
      reply({
        id: request?.id ?? "",
        error: error instanceof Error ? error.message : String(error),
      });
    }
  }
}
