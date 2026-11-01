#!/usr/bin/env node
"use strict";

// Runs the Go binary from the platform package npm picked via optionalDependencies.
const { spawn } = require("node:child_process");

const pkg = `postgres-mcp-safe-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "postgres-mcp-safe.exe" : "postgres-mcp-safe";

let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  console.error(
    `postgres-mcp-safe: no binary for ${process.platform}-${process.arch} (${pkg} is not installed).\n` +
      "Supported: linux-x64, linux-arm64, darwin-x64, darwin-arm64, win32-x64. " +
      "If you installed with --no-optional, reinstall without it.",
  );
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => child.kill(sig));
}
child.on("error", (err) => {
  console.error(`postgres-mcp-safe: ${err.message}`);
  process.exit(1);
});
child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 1);
});
