// Builds the npm packages from GoReleaser's dist/ into npm/out/:
// one package per platform holding its binary, plus the main package whose
// optionalDependencies pull in the right one (esbuild's pattern, SPEC §9).
// Also lays out an MCPB bundle (for Smithery and Claude Desktop) in
// npm/out/mcpb: the same launcher with all platform packages beside it, since
// MCPB can pick an OS but not a CPU architecture.
//
//   node npm/build.mjs <version>
import { chmodSync, copyFileSync, cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const version = process.argv[2]?.replace(/^v/, "");
if (!version) {
  console.error("usage: node npm/build.mjs <version>");
  process.exit(1);
}

// GOOS/GOARCH -> Node's process.platform/process.arch.
const platforms = { linux: "linux", darwin: "darwin", windows: "win32" };
const archs = { amd64: "x64", arm64: "arm64" };

const main = JSON.parse(readFileSync("npm/postgres-mcp-safe/package.json", "utf8"));
const out = "npm/out";
rmSync(out, { recursive: true, force: true });

const binaries = JSON.parse(readFileSync("dist/artifacts.json", "utf8")).filter((a) => a.type === "Binary");
if (binaries.length !== 5) { // linux/darwin x64+arm64, win32 x64
  console.error(`expected 5 binaries in dist/artifacts.json, found ${binaries.length}`);
  process.exit(1);
}
main.optionalDependencies = {};
for (const a of binaries) {
  const name = `${main.name}-${platforms[a.goos]}-${archs[a.goarch]}`;
  const exe = a.goos === "windows" ? "postgres-mcp-safe.exe" : "postgres-mcp-safe";
  const dir = join(out, name);
  mkdirSync(join(dir, "bin"), { recursive: true });
  copyFileSync(a.path, join(dir, "bin", exe));
  chmodSync(join(dir, "bin", exe), 0o755);
  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify(
      {
        name,
        version,
        description: `${platforms[a.goos]}-${archs[a.goarch]} binary for ${main.name}`,
        repository: main.repository,
        license: main.license,
        os: [platforms[a.goos]],
        cpu: [archs[a.goarch]],
        files: ["bin"],
        preferUnplugged: true,
      },
      null,
      2,
    ) + "\n",
  );
  main.optionalDependencies[name] = version;
}

const mainDir = join(out, main.name);
mkdirSync(join(mainDir, "bin"), { recursive: true });
copyFileSync("npm/postgres-mcp-safe/bin/postgres-mcp-safe.js", join(mainDir, "bin", "postgres-mcp-safe.js"));
chmodSync(join(mainDir, "bin", "postgres-mcp-safe.js"), 0o755);
copyFileSync("README.md", join(mainDir, "README.md"));
copyFileSync("LICENSE", join(mainDir, "LICENSE"));
writeFileSync(join(mainDir, "package.json"), JSON.stringify({ ...main, version }, null, 2) + "\n");

const bundle = join(out, "mcpb");
for (const name of Object.keys(main.optionalDependencies)) {
  cpSync(join(out, name), join(bundle, "server", "node_modules", name), { recursive: true });
}
copyFileSync("npm/postgres-mcp-safe/bin/postgres-mcp-safe.js", join(bundle, "server", "postgres-mcp-safe.js"));
copyFileSync("LICENSE", join(bundle, "LICENSE"));
const manifest = JSON.parse(readFileSync("npm/mcpb/manifest.json", "utf8"));
writeFileSync(join(bundle, "manifest.json"), JSON.stringify({ ...manifest, version }, null, 2) + "\n");
console.log(`built ${binaries.length + 1} packages and an MCPB bundle dir in ${out}`);
