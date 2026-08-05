import { isAbsolute, join, normalize, relative, resolve } from "node:path";
import { z } from "zod";

const inputSchema = z.object({}).strict();
const outputSchema = z.object({
  passed: z.boolean(),
  filesChecked: z.number().int().nonnegative(),
  issues: z.array(z.string()),
}).strict();

const required = [
  "README.md",
  "docs/vision.md",
  "docs/product-spec.md",
  "docs/glossary.md",
  "docs/workbench/status.md",
];

function markdownFiles(root: string): string[] {
  const found: string[] = [];
  const visit = (directory: string) => {
    for (
      const entry of [...Deno.readDirSync(directory)].sort((a, b) =>
        a.name.localeCompare(b.name)
      )
    ) {
      const path = join(directory, entry.name);
      if (entry.isDirectory && !entry.isSymlink) visit(path);
      else if (entry.isFile && entry.name.endsWith(".md")) found.push(path);
      if (found.length >= 128) return;
    }
  };
  try {
    if (Deno.statSync(join(root, "docs")).isDirectory) {
      visit(join(root, "docs"));
    }
  } catch { /* reported by the required-file check */ }
  try {
    if (Deno.statSync(join(root, "README.md")).isFile) {
      found.unshift(join(root, "README.md"));
    }
  } catch { /* reported by the required-file check */ }
  return found.slice(0, 128);
}

export default {
  description: "Check required project docs and bounded local Markdown links.",
  inputSchema,
  outputSchema,
  execute() {
    const root = Deno.cwd();
    const issues: string[] = [];
    for (const path of required) {
      try {
        if (!Deno.statSync(join(root, path)).isFile) {
          issues.push(`missing ${path}`);
        }
      } catch {
        issues.push(`missing ${path}`);
      }
    }
    let total = 0;
    const files = markdownFiles(root);
    for (const file of files) {
      const data = Deno.readTextFileSync(file);
      total += new TextEncoder().encode(data).byteLength;
      if (total > 2 * 1024 * 1024) {
        issues.push("Markdown exceeds the 2 MiB check limit");
        break;
      }
      for (const match of data.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
        const target = match[1].split("#", 1)[0].trim();
        if (!target || /^[a-z]+:/i.test(target) || target.startsWith("#")) {
          continue;
        }
        const resolved = resolve(join(file, ".."), target);
        if (
          isAbsolute(target) || relative(root, resolved).startsWith("..") ||
          normalize(resolved) !== resolved
        ) {
          issues.push(
            `${relative(root, file)}: link escapes workspace: ${target}`,
          );
          continue;
        }
        try {
          Deno.statSync(resolved);
        } catch {
          issues.push(`${relative(root, file)}: missing link target ${target}`);
        }
        if (issues.length >= 64) break;
      }
      if (issues.length >= 64) break;
    }
    return { passed: issues.length === 0, filesChecked: files.length, issues };
  },
};
