#!/usr/bin/env node

/**
 * UserPromptSubmit Hook Orchestrator
 *
 * Adapted from Chefy repo, simplified for single-skill setups.
 * Original patterns from:
 * - diet103/claude-code-infrastructure-showcase (auto-activating skills)
 * - severity1/claude-code-prompt-improver (prompt clarity)
 * - jefflester/claude-skills-supercharged (AI-driven detection)
 *
 * Flow:
 * 1. Check bypass prefixes (*, /, #)
 * 2. Evaluate prompt clarity
 * 3. Keyword matching on user prompt
 * 4. AI skill detection (optional, requires ANTHROPIC_API_KEY)
 * 5. Session deduplication
 * 6. Affinity resolution
 * 7. Skill loading
 *
 * NOTE: File pattern matching removed (not useful for single-skill setups)
 */

const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

// Get workspace root from Claude Code environment variable
const WORKSPACE_ROOT = process.env.CLAUDE_PROJECT_DIR || process.cwd();

// Configuration
const SKILLS_DIR = path.join(WORKSPACE_ROOT, ".claude/skills");
const CACHE_DIR = path.join(WORKSPACE_ROOT, ".claude/.cache");
const INTENT_CACHE_DIR = path.join(CACHE_DIR, "intent-analysis");
const SESSION_FILE = path.join(CACHE_DIR, "session-skills.json");
const PERF_LOG = path.join(CACHE_DIR, "performance.log");
const SKILL_RULES_FILE = path.join(SKILLS_DIR, "skill-rules.json");

// TTL for AI intent cache (1 hour in ms)
const CACHE_TTL = 60 * 60 * 1000;

// Ensure cache directories exist
function ensureCacheDirs() {
  [CACHE_DIR, INTENT_CACHE_DIR].forEach((dir) => {
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
  });
}

// Log performance metrics
function logPerformance(metric, duration) {
  const timestamp = new Date().toISOString();
  const log = `${timestamp} | ${metric}: ${duration}ms\n`;
  fs.appendFileSync(PERF_LOG, log);
}

// Load skill rules configuration
function loadSkillRules() {
  const start = Date.now();

  if (!fs.existsSync(SKILL_RULES_FILE)) {
    console.error(`ERROR: skill-rules.json not found at ${SKILL_RULES_FILE}`);
    return { skills: [] };
  }

  const rules = JSON.parse(fs.readFileSync(SKILL_RULES_FILE, "utf8"));
  logPerformance("load-skill-rules", Date.now() - start);

  return rules;
}

// Load session state
function loadSession() {
  if (!fs.existsSync(SESSION_FILE)) {
    return {
      sessionId: generateSessionId(),
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

// Generate session ID
function generateSessionId() {
  return crypto.randomBytes(16).toString("hex");
}

// Check bypass prefixes (severity1 pattern)
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

// Evaluate prompt clarity (severity1 pattern)
function evaluateClarity(prompt) {
  const start = Date.now();

  // Simple clarity heuristics (can be enhanced with AI later)
  const hasActionVerb =
    /\b(implement|create|add|fix|update|refactor|write|build)\b/i.test(prompt);
  const hasSpecifics = /\b(function|class|component|file|in|for|to)\b/i.test(
    prompt,
  );
  const isQuestion = prompt.trim().endsWith("?");
  const isVague =
    /\b(something|stuff|thing|it|this|that)\b/i.test(prompt) &&
    prompt.split(" ").length < 10;

  const clarity = {
    hasActionVerb,
    hasSpecifics,
    isQuestion,
    isVague,
    isClear:
      (hasActionVerb && hasSpecifics && !isVague) ||
      (!isQuestion && hasSpecifics && !isVague),
  };

  logPerformance("evaluate-clarity", Date.now() - start);

  return clarity;
}

// Pattern matching on keywords and content (file pattern matching removed for simplicity)
function patternMatchSkills(editedFiles, userPrompt, skillRules) {
  const start = Date.now();
  const matches = [];

  for (const skill of skillRules.skills) {
    let confidence = 0.0;
    const reasons = [];

    // NOTE: File pattern matching removed - not useful for single-skill setups
    // Content pattern matching still available if editedFiles are tracked

    // Content pattern matching
    if (skill.triggers.contentPatterns && editedFiles.length > 0) {
      const contentMatches = [];
      for (const file of editedFiles) {
        try {
          const fullPath = path.join(WORKSPACE_ROOT, file);
          if (fs.existsSync(fullPath)) {
            const content = fs.readFileSync(fullPath, "utf8");
            for (const pattern of skill.triggers.contentPatterns) {
              if (new RegExp(pattern).test(content)) {
                contentMatches.push(pattern);
              }
            }
          }
        } catch (error) {
          // File might be unreadable - skip gracefully
        }
      }

      if (contentMatches.length > 0) {
        confidence += 0.4;
        reasons.push(
          `Matched content patterns: ${[...new Set(contentMatches)].join(", ")}`,
        );
      }
    }

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

    // Anti-pattern detection
    if (skill.triggers.antiPatterns && editedFiles.length > 0) {
      const antiPatternMatches = [];
      for (const file of editedFiles) {
        try {
          const fullPath = path.join(WORKSPACE_ROOT, file);
          if (fs.existsSync(fullPath)) {
            const content = fs.readFileSync(fullPath, "utf8");
            for (const pattern of skill.triggers.antiPatterns) {
              if (new RegExp(pattern).test(content)) {
                antiPatternMatches.push(pattern);
              }
            }
          }
        } catch (error) {
          // File might be unreadable - skip gracefully
        }
      }

      if (antiPatternMatches.length > 0) {
        confidence += 0.5; // High confidence for guardrails
        reasons.push(
          `Anti-patterns detected: ${antiPatternMatches.length} violations`,
        );
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

  logPerformance("pattern-matching", Date.now() - start);

  return matches.sort((a, b) => b.confidence - a.confidence);
}

// Cache management for AI intent analysis
function getCacheKey(prompt, files) {
  const content = `${prompt}|${files.join(",")}`;
  return crypto.createHash("md5").update(content).digest("hex");
}

function getCachedIntent(key) {
  const cachePath = path.join(INTENT_CACHE_DIR, `${key}.json`);

  if (!fs.existsSync(cachePath)) {
    return null;
  }

  const cached = JSON.parse(fs.readFileSync(cachePath, "utf8"));

  // Check TTL
  if (Date.now() - cached.timestamp > CACHE_TTL) {
    fs.unlinkSync(cachePath);
    return null;
  }

  return cached.result;
}

function setCachedIntent(key, result) {
  const cachePath = path.join(INTENT_CACHE_DIR, `${key}.json`);
  fs.writeFileSync(
    cachePath,
    JSON.stringify({ result, timestamp: Date.now() }, null, 2),
  );
}

// AI-driven skill detection (jefflester pattern)
async function aiMatchSkills(prompt, editedFiles, skillRules) {
  const start = Date.now();

  // Check cache first
  const cacheKey = getCacheKey(prompt, editedFiles);
  const cached = getCachedIntent(cacheKey);

  if (cached) {
    logPerformance("ai-matching-cached", Date.now() - start);
    return cached;
  }

  // Check if Anthropic API key is available
  const apiKey = process.env.ANTHROPIC_API_KEY;
  if (!apiKey) {
    console.error(
      "ANTHROPIC_API_KEY not set, falling back to pattern matching only",
    );
    return [];
  }

  try {
    // Use dynamic import for Anthropic SDK
    const { Anthropic } = await import("@anthropic-ai/sdk");
    const anthropic = new Anthropic({ apiKey });

    // Build skill catalog for AI
    const skillCatalog = skillRules.skills.map((skill) => ({
      id: skill.id,
      name: skill.name,
      type: skill.type,
      keywords: skill.triggers.keywords || [],
      description: `${skill.name} - ${skill.type} skill`,
    }));

    // Call Claude Haiku for intent analysis
    const response = await anthropic.messages.create({
      model: process.env.CLAUDE_CHAT_MODEL || "claude-3-5-haiku-latest",
      max_tokens: 500,
      messages: [
        {
          role: "user",
          content: `Analyze this user prompt and edited files to determine which skills are relevant.

User Prompt: "${prompt}"

Edited Files: ${editedFiles.join(", ") || "none"}

Available Skills:
${JSON.stringify(skillCatalog, null, 2)}

Return ONLY a JSON array with confidence scores (0.0-1.0) for each relevant skill:
[
  { "skillId": "skill-id", "confidence": 0.85, "reason": "explanation" }
]

Only include skills with confidence >= 0.40.`,
        },
      ],
    });

    // Parse response
    const content = response.content[0].text;
    const jsonMatch = content.match(/\[[\s\S]*\]/);
    const matches = jsonMatch ? JSON.parse(jsonMatch[0]) : [];

    // Cache the result
    setCachedIntent(cacheKey, matches);

    logPerformance("ai-matching-api-call", Date.now() - start);

    return matches;
  } catch (error) {
    console.error("AI matching error:", error.message);
    logPerformance("ai-matching-error", Date.now() - start);
    return [];
  }
}

// Combine pattern matching and AI matching
function combineMatches(patternMatches, aiMatches) {
  const combined = new Map();

  // Add pattern matches
  for (const match of patternMatches) {
    combined.set(match.skillId, {
      skillId: match.skillId,
      patternConfidence: match.confidence,
      aiConfidence: 0.0,
      reasons: [...match.reasons],
      skill: match.skill,
    });
  }

  // Add or enhance with AI matches
  for (const match of aiMatches) {
    if (combined.has(match.skillId)) {
      const existing = combined.get(match.skillId);
      existing.aiConfidence = match.confidence;
      existing.reasons.push(match.reason);
    } else {
      // AI found a skill that pattern matching didn't
      // Need to look up the skill from rules
      const skillRules = loadSkillRules();
      const skill = skillRules.skills.find((s) => s.id === match.skillId);

      if (skill) {
        combined.set(match.skillId, {
          skillId: match.skillId,
          patternConfidence: 0.0,
          aiConfidence: match.confidence,
          reasons: [match.reason],
          skill,
        });
      }
    }
  }

  // Calculate final confidence (weighted average, AI weighted higher)
  const results = Array.from(combined.values()).map((match) => {
    const finalConfidence =
      match.patternConfidence * 0.3 + match.aiConfidence * 0.7;
    return {
      ...match,
      finalConfidence,
    };
  });

  return results.sort((a, b) => b.finalConfidence - a.finalConfidence);
}

// Session deduplication (jefflester pattern)
function deduplicateSkills(matches, session) {
  return matches.filter((match) => {
    return !session.loadedSkills.includes(match.skillId);
  });
}

// Resolve affinities (jefflester pattern)
function resolveAffinities(matches, skillRules, session) {
  const toLoad = [];

  for (const match of matches) {
    // Check if skill should be loaded
    const shouldLoad = match.finalConfidence >= match.skill.confidence.required;

    if (shouldLoad) {
      toLoad.push(match);

      // Check affinities
      if (match.skill.affinities) {
        for (const affinityId of match.skill.affinities) {
          // Skip if already loaded or already in toLoad
          if (
            session.loadedSkills.includes(affinityId) ||
            toLoad.some((m) => m.skillId === affinityId)
          ) {
            continue;
          }

          // Find the affinity skill
          const affinitySkill = skillRules.skills.find(
            (s) => s.id === affinityId,
          );
          if (!affinitySkill) continue;

          // Check if affinity threshold met
          const affinityMatch = matches.find((m) => m.skillId === affinityId);
          const affinityConfidence = affinityMatch
            ? affinityMatch.finalConfidence
            : 0.0;

          if (affinityConfidence >= match.skill.affinity_threshold) {
            toLoad.push({
              skillId: affinityId,
              finalConfidence: affinityConfidence,
              reasons: ["Affinity loading"],
              skill: affinitySkill,
              affinity: true,
            });
          }
        }
      }
    }
  }

  return toLoad;
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

  const totalStart = Date.now();

  // Read user prompt from stdin
  let userPrompt = "";
  for await (const chunk of process.stdin) {
    userPrompt += chunk;
  }
  userPrompt = userPrompt.trim();

  // Check bypass prefixes
  const bypassCheck = checkBypassPrefixes(userPrompt);

  if (bypassCheck.bypass) {
    // Log bypass and pass through
    console.log(`<!-- Bypass: ${bypassCheck.type} -->`);
    console.log(bypassCheck.newPrompt);
    logPerformance("total-with-bypass", Date.now() - totalStart);
    return;
  }

  // Evaluate clarity
  const clarity = evaluateClarity(userPrompt);

  if (!clarity.isClear) {
    // Suggest using prompt-improver skill
    console.log(
      "<!-- Vague prompt detected - Consider being more specific -->",
    );
    // For Phase 1, we'll just log this. Phase 2 will implement the full clarity check.
  }

  // Load session FIRST (needed for editedFiles)
  const session = loadSession();

  // Get edited files from session state (tracked by PostToolUse hook)
  const editedFiles = session.editedFiles || [];

  // Load skill rules
  const skillRules = loadSkillRules();

  // Pattern matching (file, content, keyword, anti-pattern)
  const patternMatches = patternMatchSkills(
    editedFiles,
    userPrompt,
    skillRules,
  );

  // AI matching (if API key available)
  const aiMatches = await aiMatchSkills(userPrompt, editedFiles, skillRules);

  // Combine matches
  const combinedMatches = combineMatches(patternMatches, aiMatches);

  // Deduplicate
  const newMatches = deduplicateSkills(combinedMatches, session);

  // Resolve affinities
  const toLoad = resolveAffinities(newMatches, skillRules, session);

  // Separate into auto-load and suggestions
  const autoLoad = toLoad.filter(
    (m) => m.finalConfidence >= m.skill.confidence.required,
  );
  const suggestions = toLoad.filter(
    (m) =>
      m.finalConfidence >= m.skill.confidence.suggested &&
      m.finalConfidence < m.skill.confidence.required,
  );

  // Output
  console.log("<!-- Skills Analysis -->");
  console.log(`<!-- Pattern Matches: ${patternMatches.length} -->`);
  console.log(`<!-- AI Matches: ${aiMatches.length} -->`);
  console.log(`<!-- Auto-Load: ${autoLoad.length} -->`);
  console.log(`<!-- Suggestions: ${suggestions.length} -->`);

  // Load skills
  if (autoLoad.length > 0) {
    console.log("\n<!-- Loading Skills -->");
    for (const match of autoLoad) {
      const content = loadSkillContent(match.skill.path);
      if (content) {
        console.log(
          `\n<!-- Skill: ${match.skill.name} (confidence: ${match.finalConfidence.toFixed(2)}) -->`,
        );
        console.log(content);

        // Update session
        session.loadedSkills.push(match.skillId);
      }
    }
  }

  // Suggest skills
  if (suggestions.length > 0) {
    console.log("\n<!-- Suggested Skills (not auto-loaded) -->");
    for (const match of suggestions) {
      console.log(
        `<!-- - ${match.skill.name} (confidence: ${match.finalConfidence.toFixed(2)}) -->`,
      );
      console.log(`<!--   Reasons: ${match.reasons.join(", ")} -->`);
    }
  }

  // Pass through original prompt
  console.log(`\n${userPrompt}`);

  // Update session
  session.timestamp = Date.now();
  saveSession(session);

  logPerformance("total-orchestration", Date.now() - totalStart);
}

// Run orchestrator
orchestrate().catch((error) => {
  console.error("Orchestrator error:", error);
  process.exit(1);
});
