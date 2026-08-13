"use strict";

const assert = require("node:assert/strict");
const path = require("node:path");
const test = require("node:test");

const { selectBinary } = require("./bin/lobbyte.js");

test("selects an absolute Apple Silicon binary path", () => {
  const root = path.resolve("/tmp/lobbyte-package");
  assert.equal(selectBinary("darwin", "arm64", root), path.join(root, "vendor", "lobbyte-darwin-arm64"));
});

test("selects an absolute Intel binary path", () => {
  const root = path.resolve("/tmp/lobbyte-package");
  assert.equal(selectBinary("darwin", "x64", root), path.join(root, "vendor", "lobbyte-darwin-x64"));
});

test("rejects unsupported operating systems clearly", () => {
  assert.throws(() => selectBinary("linux", "x64", "/tmp/package"), /supports macOS 12 or newer/);
});

test("rejects unsupported macOS architectures clearly", () => {
  assert.throws(() => selectBinary("darwin", "riscv64", "/tmp/package"), /does not support the macOS riscv64 architecture/);
});
