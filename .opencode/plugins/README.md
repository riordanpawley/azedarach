# AZ OpenCode Plugin

Canonical source: `./opencode-az.js`

This follows OpenCode's documented plugin directories:
- project-local: `.opencode/plugins/`
- global: `~/.config/opencode/plugins/`

## Install (compiled az CLI)

```bash
az opencode plugin install --project-dir ~/prog/azedarach
az opencode plugin install --project-dir ~/prog/Chefy
az opencode plugin install --project-dir ~/prog/wedding
```

This installs:
1. Install global plugin: `~/.config/opencode/plugins/opencode-az.js`
2. Copy `opencode-az.js` into each repo's `.opencode/plugins/` when `--project-dir` is provided
3. Remove legacy `.opencode/plugins/opencode-linear-cli.js` when present

No standalone installer script is required; use `az opencode plugin install`.
