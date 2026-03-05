/**
 * Canonical OpenCode plugin for az issue workflows.
 *
 * This file is the source of truth.
 * Install it via:
 *   az opencode plugin install
 */

const AZ_PRIMER_USAGE = `## AZ Issue Harness

Use the \`bash\` tool for all issue-tracking operations with \`az\`.

Start each session with:
- \`az prime\`

Use \`az issue --help\` for the current command surface.
Follow the workflow guidance emitted by \`az prime\` for this repo.`;

const AZ_GUIDANCE = `<az-guidance>
${AZ_PRIMER_USAGE}
</az-guidance>`;

const AZ_TASK_AGENT_PROMPT = `${AZ_PRIMER_USAGE}

You are an az-task-agent.
- Start delegated tasks by running \`az prime\`.
- Use \`az issue\` for issue tracking operations.
- Keep issue status and notes current while executing work.
- Return concise summaries and avoid dumping raw JSON unless requested.`;

const AZ_COMMANDS = {
  "az:prime": {
    description: "Generate current az prime context",
    template:
      "Use bash and run `az prime`. Summarize the key workflow guidance and active issue-tracking expectations for this repo.",
  },
  "az:issue": {
    description: "Run az issue operations",
    template:
      "Use bash and run `az issue $ARGUMENTS`. If arguments are missing or ambiguous, run `az issue --help` first and then ask for the missing details.",
  },
  "az:workflow": {
    description: "Show az issue workflow guidance",
    template:
      "Use bash and run `az prime`. Explain the workflow guidance that it prints, then reference `az issue --help` for command details.",
  },
};

const AZ_AGENTS = {
  "az-task-agent": {
    description: "Issue workflow task agent (az backend)",
    prompt: AZ_TASK_AGENT_PROMPT,
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

async function canUseAz($) {
  try {
    const out = await $`az --version`.text();
    return Boolean(out && out.trim());
  } catch {
    return false;
  }
}

async function runAzText(commandPromiseFactory) {
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

async function buildAzContext($) {
  const [primer, issueHelp] = await Promise.all([
    runAzText(() => $`az prime`.text()),
    runAzText(() => $`az issue --help`.text()),
  ]);

  const blocks = [section("az prime", primer), section("az issue --help", issueHelp)].filter(Boolean);

  if (blocks.length === 0) {
    return "";
  }

  return `<az-context>\n${blocks.join("\n\n")}\n</az-context>\n\n${AZ_GUIDANCE}`;
}

async function injectAzContext(client, $, sessionID, context) {
  if (!(await canUseAz($))) {
    return;
  }

  const issueContext = await buildAzContext($);
  if (!issueContext) {
    return;
  }

  await client.session.prompt({
    path: { id: sessionID },
    body: {
      noReply: true,
      model: context && context.model,
      agent: context && context.agent,
      parts: [{ type: "text", text: issueContext, synthetic: true }],
    },
  });
}

export const OpencodeAzPlugin = async ({ client, $ }) => {
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
            return parts.some((part) => part.type === "text" && String(part.text || "").includes("<az-context>"));
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

      await injectAzContext(client, $, sessionID, {
        model: output.message.model,
        agent: output.message.agent,
      });
    },

    event: async ({ event }) => {
      if (event.type === "session.compacted") {
        const sessionID = event.properties.sessionID;
        const context = await getSessionContext(client, sessionID);
        await injectAzContext(client, $, sessionID, context);
      }
    },

    config: async (config) => {
      config.command = { ...(config.command || {}), ...AZ_COMMANDS };
      config.agent = { ...(config.agent || {}), ...AZ_AGENTS };
    },
  };
};
