#!/usr/bin/env node

const fs = require("node:fs");
const path = require("node:path");
const { execSync } = require("node:child_process");

const WORKSPACE_ROOT = process.env.CLAUDE_PROJECT_DIR || process.cwd();
const MODE = (process.argv[2] || "session-start").toLowerCase();

function hasBr() {
  try {
    execSync("command -v br", {
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

function hasBeadsWorkspace() {
  return fs.existsSync(path.join(WORKSPACE_ROOT, ".beads"));
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

function printCommentLines(title, content) {
  if (!content) {
    return;
  }

  console.log(`<!-- ${title} -->`);
  for (const line of content.split(/\r?\n/)) {
    if (!line || line.trim().length === 0) {
      continue;
    }
    console.log(`<!-- ${line} -->`);
  }
}

function onSessionStart() {
  const stats = run("br stats --no-color --allow-stale");
  const ready = run("br ready --limit 8 --no-color --allow-stale");
  const inProgress = run(
    "br list --status in_progress --limit 8 --no-color --allow-stale",
  );

  if (!stats && !ready && !inProgress) {
    return;
  }

  console.log("<!-- br context snapshot -->");
  printCommentLines("br stats", stats);
  printCommentLines("br ready", ready);
  printCommentLines("br in-progress", inProgress);
  console.log("<!-- end br context snapshot -->");
}

function onStop() {
  const syncOutput = run("br sync --flush-only --no-color --allow-stale", 15000);

  if (!syncOutput) {
    return;
  }

  printCommentLines("br sync --flush-only", syncOutput);
  console.log(
    "<!-- Reminder: git add .beads/ && git commit -m \"sync beads\" -->",
  );
}

function main() {
  if (!hasBr() || !hasBeadsWorkspace()) {
    process.exit(0);
  }

  if (MODE === "stop") {
    onStop();
    process.exit(0);
  }

  onSessionStart();
}

main();
