#!/usr/bin/env node

/**
 * Type Safety Hook - PreToolUse Event
 *
 * BLOCKS Edit/Write operations that contain type casting anti-patterns.
 * Enforces CLAUDE.md Critical Rule #1: NEVER use 'as' casting or 'any'.
 *
 * Scope: Only checks TypeScript files (.ts, .tsx)
 *
 * Flow:
 * 1. Check if tool is Edit or Write
 * 2. Check if file is TypeScript (.ts, .tsx) - skip all other extensions
 * 3. Scan new_string/content for type casting patterns
 * 4. Skip patterns found inside comments (// or /* */)
 * 5. Skip patterns in import/export statements (aliasing, not casting)
 * 6. If found, BLOCK the edit with detailed error message
 * 7. Suggest correct patterns (Schema.decode, type annotation)
 *
 * Anti-patterns detected:
 * - `as SomeType` (type casting)
 * - `as any` (any casting)
 * - `: any` (any type annotation - equally bad as casting)
 */

const TYPE_CASTING_PATTERNS = [
  {
    // Type casting: `as SomeType`
    // EXCLUDE: `type X as Y` (export type alias syntax)
    regex: /(?<!type\s+[A-Z][a-zA-Z0-9_]*\s+)\bas\s+([A-Z][a-zA-Z0-9_]*)/g,
    name: "Type casting with 'as'",
    severity: "CRITICAL",
  },
  {
    // Any casting: `as any`
    regex: /\bas\s+any\b/g,
    name: "Casting to 'any'",
    severity: "CRITICAL",
  },
  {
    // Any type annotation - equally bad as casting to any
    regex: /:\s*any\b(?!\s*\/\/\s*biome-ignore)/g, // Allow if biome-ignore comment
    name: "Any type annotation",
    severity: "CRITICAL",
  },
];

/**
 * Check if a line is an import or export statement
 */
function isImportOrExport(line) {
  const trimmed = line.trim();
  return (
    trimmed.startsWith("import ") ||
    trimmed.startsWith("export ") ||
    trimmed.startsWith("import{") ||
    trimmed.startsWith("export{")
  );
}

/**
 * Check if a position in code is inside a comment
 * Handles both single-line (//) and multi-line (/* */) comments
 */
function isInsideComment(code, matchIndex) {
  // Check the line for single-line comment
  const lineStart = code.lastIndexOf("\n", matchIndex) + 1;
  const lineUpToMatch = code.substring(lineStart, matchIndex);

  // Check if there's a // before the match on this line
  const singleLineCommentIndex = lineUpToMatch.indexOf("//");
  if (singleLineCommentIndex !== -1) {
    // Make sure the // isn't inside a string
    const beforeComment = lineUpToMatch.substring(0, singleLineCommentIndex);
    const quoteCount = (beforeComment.match(/"/g) || []).length;
    const singleQuoteCount = (beforeComment.match(/'/g) || []).length;
    const backtickCount = (beforeComment.match(/`/g) || []).length;

    // If even number of quotes, the // is not inside a string
    if (quoteCount % 2 === 0 && singleQuoteCount % 2 === 0 && backtickCount % 2 === 0) {
      return true;
    }
  }

  // Check for multi-line comments
  // Find the last /* before matchIndex
  let searchStart = 0;
  let lastOpenComment = -1;
  let lastCloseComment = -1;

  while (true) {
    const openIdx = code.indexOf("/*", searchStart);
    if (openIdx === -1 || openIdx >= matchIndex) break;
    lastOpenComment = openIdx;
    searchStart = openIdx + 2;
  }

  if (lastOpenComment !== -1) {
    // Find the closing */ after the last /*
    lastCloseComment = code.indexOf("*/", lastOpenComment + 2);

    // If no closing or closing is after our match, we're inside a comment
    if (lastCloseComment === -1 || lastCloseComment > matchIndex) {
      return true;
    }
  }

  return false;
}

/**
 * Scan code for type casting patterns
 */
function scanForTypeCasting(code) {
  const violations = [];
  const lines = code.split("\n");

  for (const pattern of TYPE_CASTING_PATTERNS) {
    const matches = [...code.matchAll(pattern.regex)];
    for (const match of matches) {
      const lineNumber = code.substring(0, match.index).split("\n").length;
      const line = lines[lineNumber - 1] || "";

      // Skip import/export statements - 'as' in those contexts is aliasing, not casting
      if (isImportOrExport(line)) {
        continue;
      }

      // Skip matches inside comments
      if (isInsideComment(code, match.index)) {
        continue;
      }

      violations.push({
        pattern: pattern.name,
        severity: pattern.severity,
        match: match[0],
        line: lineNumber,
      });
    }
  }

  return violations;
}

/**
 * Format violation message with examples
 */
function formatViolations(violations, toolName, filePath) {
  const critical = violations.filter((v) => v.severity === "CRITICAL");

  let message = `\n${"=".repeat(80)}\n`;
  message += "❌ TYPE SAFETY VIOLATION DETECTED\n";
  message += `${"=".repeat(80)}\n\n`;

  message += `Tool: ${toolName}\n`;
  message += `File: ${filePath}\n\n`;

  message += "CLAUDE.md Critical Rule #1:\n";
  message += `"NEVER use 'as' casting or 'any'."\n\n`;

  if (critical.length > 0) {
    message += `🚫 CRITICAL VIOLATIONS (${critical.length}):\n\n`;
    for (const v of critical) {
      message += `  Line ${v.line}: ${v.pattern}\n`;
      message += `    Found: ${v.match}\n`;
    }
    message += "\n";
  }

  message += `${"─".repeat(80)}\n`;
  message += `✅ CORRECT APPROACHES:\n\n`;

  message += "1. **Schema validation at boundary** - Use Effect Schema:\n";
  message += "   const decoded = Schema.decodeUnknownSync(MySchema)(data)\n\n";

  message += "2. **Already validated** - Use type annotation:\n";
  message += "   const userId: UserId = validated.userId  // Schema already decoded\n\n";

  message += "3. **Schema class** - Use .make():\n";
  message += "   const task = Task.make({ title: 'foo', ... })\n\n";

  message += "4. **Effect pipe** - Use proper type inference:\n";
  message += "   pipe(data, Schema.decodeUnknown(MySchema))\n\n";

  message += `${"─".repeat(80)}\n`;
  message += "❌ NEVER DO THIS:\n\n";

  message += `   const id = "" as UserId              // ❌ Casting\n`;
  message += "   const data = parsed as any           // ❌ Any casting\n";
  message += "   const value: any = something         // ❌ Any type\n\n";

  message += `${"=".repeat(80)}\n`;
  message += "This edit has been BLOCKED to enforce type safety.\n";
  message += "Please rewrite using correct patterns shown above.\n";
  message += `${"=".repeat(80)}\n`;

  return message;
}

/**
 * Debug logging helper
 */
const fs = require("node:fs");
const DEBUG_LOG = "/tmp/type-safety-hook-debug.log";

function debugLog(message) {
  const timestamp = new Date().toISOString();
  fs.appendFileSync(DEBUG_LOG, `[${timestamp}] ${message}\n`);
}

/**
 * Main hook handler
 */
function main() {
  try {
    debugLog("=== HOOK INVOKED ===");

    // Read stdin for hook payload
    const input = require("node:fs").readFileSync(0, "utf-8");
    debugLog(`Raw input length: ${input.length}`);

    const payload = JSON.parse(input);
    debugLog(`Payload keys: ${Object.keys(payload).join(", ")}`);

    // Claude Code uses snake_case for hook payloads
    const { tool_name: toolName, tool_input: toolInput } = payload;
    debugLog(`Tool: ${toolName}`);

    // Only check Edit and Write tools
    if (toolName !== "Edit" && toolName !== "Write") {
      debugLog(`Skipping non-Edit/Write tool: ${toolName}`);
      process.exit(0); // Allow other tools
    }

    // Get code to check
    let codeToCheck = null;
    let filePath = null;

    if (toolName === "Edit") {
      codeToCheck = toolInput.new_string;
      filePath = toolInput.file_path;
      debugLog(`Edit tool - file: ${filePath}`);
      debugLog(`Code length: ${codeToCheck?.length || 0}`);
    } else if (toolName === "Write") {
      codeToCheck = toolInput.content;
      filePath = toolInput.file_path;
      debugLog(`Write tool - file: ${filePath}`);
      debugLog(`Code length: ${codeToCheck?.length || 0}`);
    }

    if (!codeToCheck) {
      debugLog("No code to check - allowing");
      process.exit(0); // No code to check
    }

    // Only check TypeScript files (.ts, .tsx)
    if (!filePath?.endsWith(".ts") && !filePath?.endsWith(".tsx")) {
      debugLog(`Skipping non-TypeScript file: ${filePath}`);
      process.exit(0); // Allow non-TS files
    }

    // Scan for violations
    const violations = scanForTypeCasting(codeToCheck);
    debugLog(`Found ${violations.length} total violations`);

    // Only block on CRITICAL violations
    const critical = violations.filter((v) => v.severity === "CRITICAL");
    debugLog(`Found ${critical.length} CRITICAL violations`);

    if (critical.length > 0) {
      debugLog(
        `BLOCKING edit - violations: ${critical.map((v) => v.pattern).join(", ")}`,
      );
      // BLOCK the edit
      console.error(formatViolations(violations, toolName, filePath));
      debugLog("Exiting with code 2 (BLOCK)");
      process.exit(2); // Exit code 2 blocks the tool (PreToolUse requirement)
    }

    // Allow edit (no critical violations)
    debugLog("No critical violations - allowing edit");
    debugLog("Exiting with code 0 (ALLOW)");
    process.exit(0);
  } catch (error) {
    // Don't block on hook errors
    debugLog(`ERROR: ${error.message}`);
    debugLog(`Stack: ${error.stack}`);
    console.error(`Type safety hook error: ${error.message}`);
    debugLog("Exiting with code 0 (ERROR - allowing)");
    process.exit(0);
  }
}

main();
