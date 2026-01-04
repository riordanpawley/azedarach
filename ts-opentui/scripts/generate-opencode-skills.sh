#!/usr/bin/env bash
# Generate OpenCode SKILL.md wrappers from Claude Code .skill.md files
#
# This script scans .claude/skills/ for *.skill.md files and creates
# corresponding .opencode/skills/{name}/SKILL.md wrappers with proper
# YAML frontmatter.
#
# Usage:
#   ./scripts/generate-opencode-skills.sh [project-dir]
#
# If project-dir is not specified, uses current directory.

set -euo pipefail

PROJECT_DIR="${1:-.}"
CLAUDE_SKILLS_DIR="$PROJECT_DIR/.claude/skills"
OPENCODE_SKILLS_DIR="$PROJECT_DIR/.opencode/skills"

if [[ ! -d "$CLAUDE_SKILLS_DIR" ]]; then
    echo "Error: No .claude/skills directory found in $PROJECT_DIR"
    exit 1
fi

mkdir -p "$OPENCODE_SKILLS_DIR"

# Find all .skill.md files
find "$CLAUDE_SKILLS_DIR" -name "*.skill.md" -type f | while read -r skill_file; do
    # Get relative path from skills dir
    rel_path="${skill_file#$CLAUDE_SKILLS_DIR/}"

    # Extract skill name (filename without .skill.md)
    skill_basename=$(basename "$skill_file" .skill.md)

    # Get parent directory name for categorization
    parent_dir=$(dirname "$rel_path")

    # Create a unique skill name: parent-basename (e.g., layers-db, workflow-beads-tracking)
    if [[ "$parent_dir" == "." ]]; then
        skill_name="$skill_basename"
    else
        # Replace / with - for nested directories
        parent_slug="${parent_dir//\//-}"
        skill_name="${parent_slug}-${skill_basename}"
    fi

    # OpenCode skill directory
    opencode_skill_dir="$OPENCODE_SKILLS_DIR/$skill_name"
    skill_md="$opencode_skill_dir/SKILL.md"

    # Skip if SKILL.md already exists and is newer than source
    if [[ -f "$skill_md" ]] && [[ "$skill_md" -nt "$skill_file" ]]; then
        echo "Skipping $skill_name (up to date)"
        continue
    fi

    mkdir -p "$opencode_skill_dir"

    # Extract first heading and first paragraph for description
    first_heading=$(grep -m1 '^#' "$skill_file" | sed 's/^#* *//' || echo "$skill_name")

    # Get first non-empty, non-heading line as description
    description=$(grep -v '^#' "$skill_file" | grep -v '^\*\*' | grep -v '^$' | head -1 | cut -c1-100 || echo "Skill documentation")

    # Ensure description is at least 20 chars
    if [[ ${#description} -lt 20 ]]; then
        description="$first_heading - development skill and guidelines"
    fi

    # Calculate relative path from opencode skill to claude skill
    # .opencode/skills/foo/SKILL.md -> .claude/skills/bar/foo.skill.md
    rel_source_path="../../../.claude/skills/$rel_path"

    # Generate SKILL.md wrapper
    cat > "$skill_md" << EOF
---
name: $skill_name
description: $description
license: MIT
metadata:
  source: ".claude/skills/$rel_path"
  generated: true
---

# $first_heading

> This skill wraps [\`.claude/skills/$rel_path\`]($rel_source_path)

For complete documentation, see the source file above.

## Quick Reference

$(head -50 "$skill_file" | tail -40)
EOF

    echo "Generated: $skill_name"
done

echo ""
echo "Done! Skills generated in $OPENCODE_SKILLS_DIR"
echo ""
echo "To use with OpenCode, add 'opencode-skills' to your plugins in opencode.json:"
echo '  "plugins": ["opencode-skills"]'
