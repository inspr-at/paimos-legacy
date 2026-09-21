import assert from "node:assert/strict";
import {
  cpSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { verifyCalendarVersionBundle } from "./verify-calendar-version-bundle.mjs";

const source = fileURLToPath(
  new URL("../frontend/src/vendor/calendar-version-display", import.meta.url),
);

test("accepts the pinned complete closure without network or a doctrine checkout", () => {
  assert.equal(
    verifyCalendarVersionBundle().revision,
    "317f872bc061576fc0b45d274d3a22f69bcd4c8a",
  );
});

for (const [name, mutate] of [
  [
    "changed renderer",
    (dir) => writeFileSync(join(dir, "version.js"), "// changed"),
  ],
  ["changed config", (dir) => writeFileSync(join(dir, "display.json"), "{}")],
  ["missing license", (dir) => rmSync(join(dir, "auto-animate-license.js"))],
  ["missing schemes", (dir) => rmSync(join(dir, "schemes.json"))],
  [
    "extra payload",
    (dir) => writeFileSync(join(dir, "unexpected.js"), "// extra"),
  ],
  [
    "self-approved manifest",
    (dir) => {
      const file = join(dir, "manifest.json");
      const manifest = JSON.parse(readFileSync(file, "utf8"));
      manifest.revision = "0".repeat(40);
      writeFileSync(file, JSON.stringify(manifest));
    },
  ],
  [
    "symlinked renderer",
    (dir) => {
      rmSync(join(dir, "version.js"));
      symlinkSync(join(source, "version.js"), join(dir, "version.js"));
    },
  ],
]) {
  test(`rejects ${name}`, () => {
    const temporary = mkdtempSync(join(tmpdir(), "paimos-version-bundle-"));
    const copy = join(temporary, "bundle");
    try {
      cpSync(source, copy, { recursive: true });
      mutate(copy);
      assert.throws(() => verifyCalendarVersionBundle(copy));
    } finally {
      rmSync(temporary, { recursive: true });
    }
  });
}
