#!/usr/bin/env node

/**
 * UserPromptSubmit Hook Orchestrator (Simplified)
 *
 * Combines patterns from:
 * - diet103/claude-code-infrastructure-showcase (auto-activating skills)
 * - severity1/claude-code-prompt-improver (prompt clarity)
 * - jefflester/claude-skills-supercharged (AI-driven detection)
 *
 * Flow:
 * 1. Check bypass prefixes (*, /, #)
 * 2. Evaluate prompt clarity
 * 3. Pattern matching on keywords
 * 4. Skill loading
 */

const fs = require("node:fs");
const path = require("node:path");

// Get workspace root from Claude Code environment variable
const WORKSPACE_ROOT = process.env.CLAUDE_PROJECT_DIR || process.cwd();

// Configuration
const SKILLS_DIR = path.join(WORKSPACE_ROOT, ".claude/skills");
const CACHE_DIR = path.join(WORKSPACE_ROOT, ".claude/.cache");
const SESSION_FILE = path.join(CACHE_DIR, "session-skills.json");
const SKILL_RULES_FILE = path.join(SKILLS_DIR, "skill-rules.json");

// Ensure cache directories exist
function ensureCacheDirs() {
  if (!fs.existsSync(CACHE_DIR)) {
    fs.mkdirSync(CACHE_DIR, { recursive: true });
  }
}

// Load skill rules configuration
function loadSkillRules() {
  if (!fs.existsSync(SKILL_RULES_FILE)) {
    return { skills: [] };
  }
  return JSON.parse(fs.readFileSync(SKILL_RULES_FILE, "utf8"));
}

// Load session state
function loadSession() {
  if (!fs.existsSync(SESSION_FILE)) {
    return {
      loadedSkills: [],
      timestamp: Date.now(),
    };
  }
  return JSON.parse(fs.readFileSync(SESSION_FILE, "utf8"));
}

// Save session state
function saveSession(session) {
  fs.writeFileSync(SESSION_FILE, JSON.stringify(session, null, 2));
}

// Check bypass prefixes
function checkBypassPrefixes(prompt) {
  const trimmed = prompt.trim();

  // * - "Just do it" - skip all checks
  if (trimmed.startsWith("*")) {
    return {
      bypass: true,
      type: "just-do-it",
      newPrompt: trimmed.substring(1).trim(),
    };
  }

  // / - Slash command - skip checks
  if (trimmed.startsWith("/")) {
    return { bypass: true, type: "slash-command", newPrompt: trimmed };
  }

  // # - Context only - add to memory, no action
  if (trimmed.startsWith("#")) {
    return { bypass: true, type: "context-only", newPrompt: trimmed };
  }

  return { bypass: false, newPrompt: trimmed };
}

// Evaluate prompt clarity
function evaluateClarity(prompt) {
  const hasActionVerb =
    /\b(implement|create|add|fix|update|refactor|write|build)\b/i.test(prompt);
  const hasSpecifics = /\b(function|class|component|file|in|for|to)\b/i.test(
    prompt,
  );
  const isQuestion = prompt.trim().endsWith("?");
  const isVague =
    /\b(something|stuff|thing|it|this|that)\b/i.test(prompt) &&
    prompt.split(" ").length < 10;

  return {
    isClear:
      (hasActionVerb && hasSpecifics && !isVague) ||
      (!isQuestion && hasSpecifics && !isVague),
  };
}

// Simple keyword matching for skills
function matchSkills(userPrompt, skillRules) {
  const matches = [];

  for (const skill of skillRules.skills) {
    let confidence = 0.0;
    const reasons = [];

    // Keyword matching
    if (skill.triggers.keywords && userPrompt) {
      const promptLower = userPrompt.toLowerCase();
      const matchedKeywords = skill.triggers.keywords.filter((keyword) =>
        promptLower.includes(keyword.toLowerCase()),
      );

      if (matchedKeywords.length > 0) {
        confidence += matchedKeywords.length * 0.2;
        reasons.push(`Matched keywords: ${matchedKeywords.join(", ")}`);
      }
    }

    if (confidence > 0) {
      matches.push({
        skillId: skill.id,
        confidence,
        reasons,
        skill,
      });
    }
  }

  return matches.sort((a, b) => b.confidence - a.confidence);
}

// Load skill content
function loadSkillContent(skillPath) {
  const fullPath = path.join(WORKSPACE_ROOT, skillPath);

  if (!fs.existsSync(fullPath)) {
    console.error(`ERROR: Skill file not found: ${fullPath}`);
    return null;
  }

  return fs.readFileSync(fullPath, "utf8");
}

// Main orchestrator
async function orchestrate() {
  ensureCacheDirs();

  // Read user prompt from stdin
  let userPrompt = "";
  for await (const chunk of process.stdin) {
    userPrompt += chunk;
  }
  userPrompt = userPrompt.trim();

  // Check bypass prefixes
  const bypassCheck = checkBypassPrefixes(userPrompt);

  if (bypassCheck.bypass) {
    console.log(`<!-- Bypass: ${bypassCheck.type} -->`);
    console.log(bypassCheck.newPrompt);
    return;
  }

  // Evaluate clarity
  const clarity = evaluateClarity(userPrompt);

  if (!clarity.isClear) {
    console.log(
      "<!-- Vague prompt detected - Consider being more specific -->",
    );
  }

  // Load session
  const session = loadSession();

  // Load skill rules
  const skillRules = loadSkillRules();

  // Match skills
  const matches = matchSkills(userPrompt, skillRules);

  // Filter already loaded skills
  const newMatches = matches.filter(
    (m) => !session.loadedSkills.includes(m.skillId),
  );

  // Auto-load skills meeting threshold
  const autoLoad = newMatches.filter(
    (m) => m.confidence >= m.skill.confidence.required,
  );

  // Output
  console.log("<!-- Skills Analysis -->");
  console.log(`<!-- Pattern Matches: ${matches.length} -->`);
  console.log(`<!-- Auto-Load: ${autoLoad.length} -->`);

  // Load skills
  if (autoLoad.length > 0) {
    console.log("\n<!-- Loading Skills -->");
    for (const match of autoLoad) {
      const content = loadSkillContent(match.skill.path);
      if (content) {
        console.log(
          `\n<!-- Skill: ${match.skill.name} (confidence: ${match.confidence.toFixed(2)}) -->`,
        );
        console.log(content);
        session.loadedSkills.push(match.skillId);
      }
    }
  }

  // Pass through original prompt
  console.log(`\n${userPrompt}`);

  // Update session
  session.timestamp = Date.now();
  saveSession(session);
}

// Run orchestrator
orchestrate().catch((error) => {
  console.error("Orchestrator error:", error);
  process.exit(1);
});
