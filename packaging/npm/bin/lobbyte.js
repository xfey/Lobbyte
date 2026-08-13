#!/usr/bin/env node

"use strict";

const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const supportedBinaries = Object.freeze({
  arm64: "lobbyte-darwin-arm64",
  x64: "lobbyte-darwin-x64",
});

function selectBinary(platform = process.platform, architecture = process.arch, packageRoot = path.resolve(__dirname, "..")) {
  if (platform !== "darwin") {
    throw new Error("Lobbyte supports macOS 12 or newer on Apple Silicon and Intel Macs.");
  }

  const filename = supportedBinaries[architecture];
  if (!filename) {
    throw new Error(`Lobbyte does not support the macOS ${architecture} architecture.`);
  }
  return path.resolve(packageRoot, "vendor", filename);
}

function run(arguments_ = process.argv.slice(2)) {
  let binary;
  try {
    binary = selectBinary();
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    return 1;
  }

  const result = spawnSync(binary, arguments_, { stdio: "inherit" });
  if (result.error) {
    process.stderr.write(`Could not start Lobbyte: ${result.error.message}\n`);
    return 1;
  }
  if (result.status !== null) {
    return result.status;
  }
  if (result.signal && os.constants.signals[result.signal]) {
    return 128 + os.constants.signals[result.signal];
  }
  return 1;
}

if (require.main === module) {
  process.exitCode = run();
}

module.exports = { run, selectBinary };
