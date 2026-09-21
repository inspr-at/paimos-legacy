import { createHash } from "node:crypto";
import { lstatSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");
const files = [
  "auto-animate-license.js",
  "auto-animate.js",
  "display.json",
  "manifest.json",
  "package.json",
  "presentation.js",
  "schemes.json",
  "version-interaction.js",
  "version.js",
];

// Expected pins live outside the upstream bundle. Never derive an expected
// digest from candidate bytes or execute bundled JavaScript to verify it.
export function verifyCalendarVersionBundle(
  directory = join(root, "frontend/src/vendor/calendar-version-display"),
) {
  const pin = JSON.parse(
    readFileSync(
      join(root, "scripts/calendar-version-bundle-pin.json"),
      "utf8",
    ),
  );
  const fail = (reason) => {
    throw new Error(`Calendar version bundle: ${reason}`);
  };
  if (
    !/^[a-f0-9]{40}$/.test(pin.revision) ||
    !/^[a-f0-9]{64}$/.test(pin.configSha256) ||
    !/^[a-f0-9]{64}$/.test(pin.manifestSha256) ||
    pin.repository !== "inspr-at/inspr"
  )
    fail("invalid pin");
  if (
    !lstatSync(directory).isDirectory() ||
    lstatSync(directory).isSymbolicLink()
  )
    fail("regular directory required");
  if (JSON.stringify(readdirSync(directory).sort()) !== JSON.stringify(files))
    fail("unexpected or missing files");
  const read = (name) => {
    const path = join(directory, name);
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 262144)
      fail(`invalid file: ${name}`);
    return readFileSync(path);
  };
  const manifestBytes = read("manifest.json");
  if (digest(manifestBytes) !== pin.manifestSha256)
    fail("manifest digest mismatch");
  const manifest = JSON.parse(manifestBytes);
  if (
    manifest.repository !== pin.repository ||
    manifest.revision !== pin.revision ||
    manifest.expectedConfigSha256 !== pin.configSha256 ||
    manifest.schema !== "inspr.calendar-version-display.v2" ||
    manifest.mode !== "build-time-only"
  )
    fail("manifest provenance mismatch");
  if (
    !Array.isArray(manifest.files) ||
    JSON.stringify(manifest.files.map((file) => file.outputPath).sort()) !==
      JSON.stringify(files.filter((name) => name !== "manifest.json"))
  )
    fail("manifest file set mismatch");
  for (const file of manifest.files) {
    const bytes = read(file.outputPath);
    if (bytes.length !== file.size || digest(bytes) !== file.sha256)
      fail(`payload mismatch: ${file.outputPath}`);
  }
  const configBytes = read("display.json");
  if (digest(configBytes) !== pin.configSha256)
    fail("configuration digest mismatch");
  const config = JSON.parse(configBytes);
  if (
    config.schema !== "inspr.calendar-version-display.v2" ||
    config.scheme !== "inspr-calendar-v2"
  )
    fail("configuration schema mismatch");
  return pin;
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    const pin = verifyCalendarVersionBundle();
    console.log(`Calendar version bundle verified offline: ${pin.revision}`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
