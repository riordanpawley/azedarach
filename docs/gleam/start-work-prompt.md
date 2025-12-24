# Start+Work Prompt Format

## Overview

When using `Space+S` (start+work) or `Space+!` (yolo mode), Claude receives a structured prompt with bead context.

## Prompt Template

```
work on bead {bead-id} ({issue_type}): {title}

Run `bd show {bead-id}` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the bead with your implementation plan using `bd update {bead-id} --design="..."`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.

[If images attached:]
Attached images (use Read tool to view):
/path/to/.beads/images/{bead-id}/abc123.png
/path/to/.beads/images/{bead-id}/def456.png
```

## Example

For bead `az-456` with title "Fix login button not responding":

```
work on bead az-456 (bug): Fix login button not responding

Run `bd show az-456` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the bead with your implementation plan using `bd update az-456 --design="..."`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.

Attached images (use Read tool to view):
/home/user/project/.beads/images/az-456/screenshot-1734567890.png
```

## Variants

### Standard Start+Work (Space+S)

Uses the full prompt above.

### Yolo Mode (Space+!)

Same prompt, but Claude is started with `--dangerously-skip-permissions` flag:

```bash
claude --dangerously-skip-permissions -p "{prompt}"
```

### Simple Start (Space+s)

No initial prompt. Just starts Claude in the worktree.

```bash
claude
```

## Key Behaviors

1. **bd show for context**: Prompt tells Claude to run `bd show` rather than including all details in the prompt. This keeps the prompt concise and ensures Claude gets the latest info.

2. **Ask questions first**: Explicitly instructs Claude to clarify before implementing. Reduces wasted work on misunderstood requirements.

3. **Update design notes**: Encourages Claude to document the plan in the bead itself, making the bead self-sufficient.

4. **Image paths**: If images are attached to the bead, their absolute paths are included so Claude can use the `Read` tool to view them.

## Implementation (Gleam)

```gleam
pub fn build_start_work_prompt(
  task: Task,
  project_path: String,
  attachments: List(Attachment),
) -> String {
  let base = "work on bead " <> task.id <> " (" <> task.issue_type <> "): " <> task.title <> "

Run `bd show " <> task.id <> "` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the bead with your implementation plan using `bd update " <> task.id <> " --design=\"...\"`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context."

  case attachments {
    [] -> base
    _ -> {
      let paths = attachments
        |> list.map(fn(a) {
          project_path <> "/.beads/images/" <> task.id <> "/" <> a.filename
        })
        |> string.join("\n")

      base <> "\n\nAttached images (use Read tool to view):\n" <> paths
    }
  }
}
```

## Configuration

The prompt is hardcoded for v1.0. Future versions may allow customization via config:

```json
{
  "session": {
    "startWorkPromptTemplate": "..."
  }
}
```
