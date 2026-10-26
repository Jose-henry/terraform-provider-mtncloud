// PostToolUse hook: keep graphify-out/ current while coding, not only on commit.
// Runs `graphify update .` (AST-only, local, no LLM) after Edit/Write on a Go file.
// Never fails the tool call: every error path exits 0.
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";

const CODE_EXT = new Set([".go"]);
// Mirrors .graphifyignore: edits here never change the graph.
const SKIP_DIRS = ["graphify-out", ".claude", "docs", "dev-docs", "examples", "templates", "dist", "bin", "testdata"];

const debug = (...args) => process.env.GRAPHIFY_HOOK_DEBUG && console.error("[graphify-hook]", ...args);

// Git Bash / MSYS paths (/c/Users/...) -> Windows paths (C:/Users/...), so path.relative works.
const toNative = (p) =>
  process.platform === "win32" ? p.replace(/^\/([a-zA-Z])\//, (_, d) => `${d.toUpperCase()}:/`) : p;

let raw = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => (raw += chunk));
process.stdin.on("end", () => {
  try {
    const input = JSON.parse(raw || "{}");
    const root = path.resolve(toNative(process.env.CLAUDE_PROJECT_DIR || input.cwd || process.cwd()));
    const files = [input.tool_input?.file_path].filter(Boolean).map(toNative);
    if (files.length === 0) return debug("no file path in tool input");

    const touchesCode = files.some((file) => {
      const rel = path.relative(root, path.resolve(root, file));
      if (rel.startsWith("..") || path.isAbsolute(rel)) return false; // outside the repo
      const parts = rel.split(/[\\/]/);
      if (parts.some((p) => SKIP_DIRS.includes(p))) return false;
      return CODE_EXT.has(path.extname(rel).toLowerCase());
    });
    if (!touchesCode) return debug("not a graphed code file:", files.join(", "));
    // Nothing to update until the graph has been built once.
    if (!existsSync(path.join(root, "graphify-out", "graph.json"))) return debug("no graph yet");

    debug("updating graph in", root);
    // --force: without it, update refuses to write a graph with fewer nodes,
    // so deleting or slimming code would leave stale nodes behind.
    // One fixed command string (no user input in it); the shell resolves graphify(.exe) via PATH.
    const result = spawnSync("graphify update . --force", {
      cwd: root,
      stdio: "ignore",
      timeout: 60_000,
      shell: true,
      env: { ...process.env, PYTHONHASHSEED: "0" }, // stable communities, matching the git hooks
    });
    debug("graphify exit", result.status, result.error?.message ?? "");
  } catch (error) {
    // Graph freshness is best-effort; never block editing.
    debug("skipped:", error instanceof Error ? error.message : error);
  }
});
