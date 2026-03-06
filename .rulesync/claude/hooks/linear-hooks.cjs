#!/usr/bin/env node

const { execSync } = require("node:child_process");

const WORKSPACE_ROOT = process.env.CLAUDE_PROJECT_DIR || process.cwd();
const MODE = (process.argv[2] || "session-start").toLowerCase();

function hasAzCli() {
  try {
    execSync("command -v az", {
      cwd: WORKSPACE_ROOT,
      stdio: "ignore",
      shell: true,
      timeout: 2000,
    });
    return true;
  } catch {
    return false;
  }
}

function run(command, timeout = 10000) {
  try {
    return execSync(command, {
      cwd: WORKSPACE_ROOT,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      timeout,
    }).trim();
  } catch {
    return "";
  }
}

function onSessionStart() {
  const primer = run("az prime", 6000);
  if (primer) {
    console.log("<!-- az prime available: run `az prime` at session start -->");
  } else {
    console.log("<!-- az CLI detected; run `az prime` for workflow guidance -->");
  }

  const issueHelp = run("az issue --help", 6000);
  if (issueHelp) {
    console.log("<!-- issue commands available via `az issue --help` -->");
  }
}

function onStop() {
  console.log("<!-- Reminder: update/close issues, then git pull --rebase && git push -->");
}

function main() {
  if (!hasAzCli()) {
    process.exit(0);
  }

  if (MODE === "stop") {
    onStop();
    process.exit(0);
  }

  onSessionStart();
}

main();
