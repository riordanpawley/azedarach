#!/usr/bin/env node

const fs = require("node:fs");
const { execSync } = require("node:child_process");

const WORKSPACE_ROOT = process.env.CLAUDE_PROJECT_DIR || process.cwd();
const MODE = (process.argv[2] || "session-start").toLowerCase();

function hasLinearCli() {
  try {
    execSync("command -v linear-cli", {
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

function getDefaultTeam() {
  try {
    const configPath = `${WORKSPACE_ROOT}/.azedarach.json`;
    if (!fs.existsSync(configPath)) return "";

    const config = JSON.parse(fs.readFileSync(configPath, "utf8"));
    return config?.issueTracker?.linear?.team || "";
  } catch {
    return "";
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
  const team = getDefaultTeam();

  if (team) {
    console.log(`<!-- linear default team: ${team} -->`);
  } else {
    console.log(
      "<!-- linear default team not configured; set issueTracker.linear.team in .azedarach.json or pass -t TEAM when creating issues -->",
    );
  }

  const reminder = run(
    "linear-cli i list --output json --compact --all 2>/dev/null",
    6000,
  );
  if (reminder) {
    console.log("<!-- linear issue list available via `linear-cli i list --output json --compact --all` -->");
  }
}

function onStop() {
  console.log("<!-- Reminder: update/close Linear issues, then git pull --rebase && git push -->");
}

function main() {
  if (!hasLinearCli()) {
    process.exit(0);
  }

  if (MODE === "stop") {
    onStop();
    process.exit(0);
  }

  onSessionStart();
}

main();
