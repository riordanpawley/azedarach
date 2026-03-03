/**
 * Local OpenCode plugin for beads_rust (br).
 * Mirrors opencode-beads behavior points:
 * - Context injection on session start and after compaction
 * - /beads:* command templates
 * - beads-task-agent subagent
 */

const BR_CLI_USAGE = `## CLI Usage

Use the \`bash\` tool for all beads_rust operations with \`br\`.

Common commands:
- \`br ready --json\` - list ready tasks
- \`br list --status in_progress --json\` - list in-progress work
- \`br show <id> --json\` - show issue details
- \`br create \"title\" -t task -p 2 --json\` - create issue
- \`br update <id> --status in_progress --json\` - update status
- \`br close <id> --reason \"message\" --json\` - close issue
- \`br dep add <from> <to> --type blocks --json\` - add dependency
- \`br blocked --json\` - show blocked issues
- \`br stats --json\` - project statistics
- \`br sync --flush-only\` - flush to JSONL (then commit .beads manually)

If unsure, run \`br <command> --help\`.
Prefer \`--json\` for structured output.`;

const BR_GUIDANCE = `<beads-guidance>
${BR_CLI_USAGE}

## Sync Behavior

\`br\` is non-invasive: it does not commit to git.
After \`br sync --flush-only\`, run:
\`git add .beads/ && git commit -m "sync beads"\`.

## Agent Delegation

For multi-step beads workflows, delegate via:
\`task\` tool with \`subagent_type: "beads-task-agent"\`.
</beads-guidance>`;

const BR_TASK_AGENT_PROMPT = `${BR_CLI_USAGE}

You are a beads-task-agent.
- For status requests, run br commands and return concise summaries.
- For execution requests, claim/update/close issues as needed.
- Do not dump raw JSON unless explicitly requested.`;

const BR_COMMANDS = {
  "beads:ready": {
    description: "List ready (unblocked) issues",
    template:
      "Use bash and run `br ready --json --limit 20`. If arguments are provided, append them after `br ready`. Summarize priorities and suggested next pick.",
  },
  "beads:list": {
    description: "List issues with filters",
    template:
      "Use bash and run `br list --json`. If arguments are provided, append them after `br list`. Summarize key rows instead of dumping raw JSON.",
  },
  "beads:show": {
    description: "Show issue details",
    template:
      "Use bash and run `br show $ARGUMENTS --json` (or ask for an issue id if missing). Summarize status, priority, dependencies, and next action.",
  },
  "beads:create": {
    description: "Create an issue",
    template:
      "Use bash and run `br create $ARGUMENTS --json` when enough details are present. If details are missing, ask for title/type/priority first.",
  },
  "beads:update": {
    description: "Update issue fields",
    template:
      "Use bash and run `br update $ARGUMENTS --json`. If issue id or changes are ambiguous, ask clarifying questions first.",
  },
  "beads:close": {
    description: "Close an issue",
    template:
      "Use bash and run `br close $ARGUMENTS --json`. Include a reason when possible and suggest checking newly unblocked work via `br ready`.",
  },
  "beads:dep": {
    description: "Manage dependencies",
    template:
      "Use bash and run `br dep $ARGUMENTS --json` (or without --json for tree output when clearer). Explain dependency impact.",
  },
  "beads:blocked": {
    description: "Show blocked issues",
    template:
      "Use bash and run `br blocked --json --limit 50` (plus any user args). Summarize blockers and likely unblock path.",
  },
  "beads:stats": {
    description: "Show project stats",
    template:
      "Use bash and run `br stats --json`. Summarize totals, in-progress count, blocked count, and high-priority focus.",
  },
  "beads:search": {
    description: "Search issues",
    template:
      "Use bash and run `br search $ARGUMENTS --json`. If no query is provided, ask for one.",
  },
  "beads:sync": {
    description: "Flush br JSONL and remind about git commit",
    template:
      "Use bash and run `br sync --flush-only`. Then remind to run `git add .beads/ && git commit -m \"sync beads\"`.",
  },
  "beads:workflow": {
    description: "Show br workflow guidance",
    template:
      "Explain the workflow: `br ready` -> `br update <id> --claim` -> implement -> `br close <id> --reason` -> `br sync --flush-only` -> commit `.beads/`.",
  },
  "beads:prime": {
    description: "Generate current br context snapshot",
    template:
      "Use bash and run: `br stats --no-color`, `br ready --limit 10 --no-color`, `br list --status in_progress --limit 10 --no-color`, and `br blocked --limit 10 --no-color`. Provide a concise summary.",
  },
};

const BR_AGENTS = {
  "beads-task-agent": {
    description: "Beads_rust task completion agent",
    prompt: BR_TASK_AGENT_PROMPT,
    mode: "subagent",
  },
};

async function getSessionContext(client, sessionID) {
  try {
    const response = await client.session.messages({
      path: { id: sessionID },
      query: { limit: 50 },
    });

    if (response.data) {
      for (const msg of response.data) {
        if (msg.info.role === "user" && "model" in msg.info && msg.info.model) {
          return { model: msg.info.model, agent: msg.info.agent };
        }
      }
    }
  } catch {
    // fall through
  }

  return undefined;
}

async function canUseBr($) {
  try {
    const out = await $`br --version`.text();
    return Boolean(out && out.trim());
  } catch {
    return false;
  }
}

async function runBrText(commandPromiseFactory) {
  try {
    const out = await commandPromiseFactory();
    return String(out || "").trim();
  } catch {
    return "";
  }
}

function section(title, content) {
  if (!content) {
    return "";
  }

  const trimmed = content.trim();
  if (!trimmed) {
    return "";
  }

  const clipped = trimmed.length > 3000 ? `${trimmed.slice(0, 3000)}\n...` : trimmed;
  return `## ${title}\n\n\`\`\`\n${clipped}\n\`\`\``;
}

async function buildBrContext($) {
  const [stats, ready, inProgress, blocked] = await Promise.all([
    runBrText(() => $`br stats --no-color --allow-stale`.text()),
    runBrText(() => $`br ready --limit 10 --no-color --allow-stale`.text()),
    runBrText(() =>
      $`br list --status in_progress --limit 10 --no-color --allow-stale`.text(),
    ),
    runBrText(() => $`br blocked --limit 10 --no-color --allow-stale`.text()),
  ]);

  const blocks = [
    section("br stats", stats),
    section("ready issues", ready),
    section("in-progress issues", inProgress),
    section("blocked issues", blocked),
  ].filter(Boolean);

  if (blocks.length === 0) {
    return "";
  }

  return `<beads-context>\n${blocks.join("\n\n")}\n</beads-context>\n\n${BR_GUIDANCE}`;
}

async function injectBrContext(client, $, sessionID, context) {
  if (!(await canUseBr($))) {
    return;
  }

  const brContext = await buildBrContext($);
  if (!brContext) {
    return;
  }

  await client.session.prompt({
    path: { id: sessionID },
    body: {
      noReply: true,
      model: context && context.model,
      agent: context && context.agent,
      parts: [{ type: "text", text: brContext, synthetic: true }],
    },
  });
}

export const BrBeadsPlugin = async ({ client, $ }) => {
  const injectedSessions = new Set();

  return {
    "chat.message": async (_input, output) => {
      const sessionID = output.message.sessionID;

      if (injectedSessions.has(sessionID)) {
        return;
      }

      try {
        const existing = await client.session.messages({
          path: { id: sessionID },
        });

        if (existing.data) {
          const alreadyInjected = existing.data.some((msg) => {
            const parts = msg.parts || (msg.info && msg.info.parts);
            if (!parts) {
              return false;
            }
            return parts.some(
              (part) => part.type === "text" && String(part.text || "").includes("<beads-context>"),
            );
          });

          if (alreadyInjected) {
            injectedSessions.add(sessionID);
            return;
          }
        }
      } catch {
        // proceed
      }

      injectedSessions.add(sessionID);

      await injectBrContext(client, $, sessionID, {
        model: output.message.model,
        agent: output.message.agent,
      });
    },

    event: async ({ event }) => {
      if (event.type === "session.compacted") {
        const sessionID = event.properties.sessionID;
        const context = await getSessionContext(client, sessionID);
        await injectBrContext(client, $, sessionID, context);
      }
    },

    config: async (config) => {
      config.command = { ...(config.command || {}), ...BR_COMMANDS };
      config.agent = { ...(config.agent || {}), ...BR_AGENTS };
    },
  };
};
