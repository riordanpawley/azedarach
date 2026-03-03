# 09 - Keybinding Reference (Normative)

This section is a full normative keybinding contract.

## 9.1 Notation

- `Space x` means press `Space`, then `x` while in action mode.
- `g x` means press `g`, then `x` while in goto mode.

## 9.2 Global Keys

| Key | Context | Required Behavior |
|---|---|---|
| `Esc` | all modal contexts | back out to previous stable mode/view |
| `q` | board/overlays | close overlay or quit/back from board context |
| `Ctrl-l` | board | force full redraw |

## 9.3 Normal Mode Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `h` | board visible | focus moves left |
| `j` | board visible | focus moves down |
| `k` | board visible | focus moves up |
| `l` | board visible | focus moves right |
| `Left` | board visible | same as `h` |
| `Down` | board visible | same as `j` |
| `Up` | board visible | same as `k` |
| `Right` | board visible | same as `l` |
| `Ctrl-Shift-d` | overflow exists | half-page down |
| `Ctrl-Shift-u` | overflow exists | half-page up |
| `Enter` | card focused | opens detail panel |
| `Space` | card focused | enters action mode |
| `,` | board visible | enters sort mode |
| `f` | board visible | enters filter mode |
| `/` | board visible | enters search mode |
| `g` | board visible | enters goto mode |
| `v` | board visible | enters select mode |
| `Tab` | board visible | toggles view |
| `r` | board visible | refreshes git/task metadata |
| `p` | board visible | opens planning overlay |
| `c` | board visible | opens manual create flow |
| `C` | board visible | opens AI create flow |
| `s` | board visible | opens settings overlay |
| `?` | board visible | opens help overlay |
| `L` | board visible | opens logs overlay/menu |

## 9.4 Goto Mode Keys

| Sequence | Preconditions | Postconditions |
|---|---|---|
| `g g` | cards in column | focus = first card in column |
| `g e` | cards in column | focus = last card in column |
| `g h` | >=1 column | focus = first column |
| `g l` | >=1 column | focus = last column |
| `g w` | visible cards | labels shown, then jump by input |
| `g p` | multiple projects configured | project selector opened |

## 9.5 Select Mode Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `a` | card focused | toggles selection for focused card |
| `A` | column focused | selects all in current column |
| `%` | any board state | selects all visible non-tombstoned |
| `*` | select mode active | invert visible non-tombstoned selection |
| `x` | select mode active | clear selection set and remain in SEL |
| `h/j/k/l` | select mode active | navigates while keeping selection set |
| `Space` | select mode active | enters action mode for selected set |
| `v` | select mode active | clear selection + return NOR |
| `Esc` | select mode active | clear selection + return NOR |

## 9.6 Search Mode Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| any printable | search mode active | query appended + live filtered view |
| `Backspace` | query non-empty | remove last char |
| `Enter` | search mode active | commit query + return NOR |
| `Esc` | search mode active | clear query + return NOR |

## 9.7 Filter Mode Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `s` | filter mode active | status submenu |
| `p` | filter mode active | priority submenu |
| `t` | filter mode active | type submenu |
| `S` | filter mode active | session submenu |
| `e` | filter mode active | toggle hide epic children |
| `1` | filter mode active | set age > 1 day |
| `7` | filter mode active | set age > 7 days |
| `3` | filter mode active | set age > 30 days |
| `0` | filter mode active | clear age filter |
| `c` | filter mode active | clear all filters |
| `Esc` | filter mode active | return NOR |

### Status Submenu

| Key | Meaning |
|---|---|
| `o` | toggle open |
| `i` | toggle in_progress |
| `b` | toggle blocked |
| `d` | toggle closed |

### Priority Submenu

| Key | Meaning |
|---|---|
| `0` | toggle P0 |
| `1` | toggle P1 |
| `2` | toggle P2 |
| `3` | toggle P3 |
| `4` | toggle P4 |

### Type Submenu

| Key | Meaning |
|---|---|
| `B` | toggle bug |
| `F` | toggle feature |
| `T` | toggle task |
| `E` | toggle epic |
| `C` | toggle chore |

### Session Submenu

| Key | Meaning |
|---|---|
| `I` | toggle idle |
| `U` | toggle busy |
| `W` | toggle waiting |
| `D` | toggle done |
| `X` | toggle error |
| `P` | toggle paused |

## 9.8 Sort Mode Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `s` | sort mode active | sort by session state |
| `p` | sort mode active | sort by priority |
| `u` | sort mode active | sort by updated timestamp |
| repeat same key | same sort active | toggle sort direction |
| `Esc` | sort mode active | cancel/exit sort mode |

## 9.9 Action Mode Keys - Session

| Sequence | Preconditions | Postconditions |
|---|---|---|
| `Space s` | issue focused | session started |
| `Space S` | issue focused | session started with work prompt |
| `Space !` | issue focused | session started in skip-permission variant |
| `Space c` | issue focused | chat session started |
| `Space a` | session exists | attached to session |
| `Space p` | busy session | session paused |
| `Space R` | paused session | session resumed |
| `Space x` | session exists | session stopped |

## 9.10 Action Mode Keys - Dev Server

| Sequence | Preconditions | Postconditions |
|---|---|---|
| `Space r` | worktree exists or creatable | dev server toggled |
| `Space v` | dev server running | attached to dev-server output |
| `Space Ctrl-r` | dev server running | dev server restarted |

## 9.11 Action Mode Keys - Git/PR

| Sequence | Preconditions | Postconditions |
|---|---|---|
| `Space u` | worktree branch exists | branch updated from configured base branch or conflict state |
| `Space f` | worktree exists | diff viewer opened |
| `Space P` | branch exists | PR created or existing surfaced |
| `Space O` | PR metadata exists | PR opened in browser |
| `Space m` | merge path valid | local merge attempted/completed |
| `Space M` | merge in progress | merge aborted |
| `Space b` | source work exists | merge-select mode then target merge |
| `Space d` | worktree exists | cleanup dialog/action executed |

### Contextual Merge Override

| Sequence | Preconditions | Postconditions |
|---|---|---|
| `Space m` | relationship-follow context with eligible upstream selected | upstream source branch merged into focused issue branch (no base-branch hop) |

## 9.12 Action Mode Keys - Edit/Authoring

| Sequence | Preconditions | Postconditions |
|---|---|---|
| `Space e` | issue focused | manual edit flow opened |
| `Space E` | issue focused | AI edit flow opened |
| `Space F` | issue focused | fork flow opened |
| `Space G` | epic focused | open epic child-board drill-down |
| `Space H` | issue focused | editor action opened in task context |
| `Space i` | issue focused | attachment overlay opened |
| `Space h` | issue focused | move issue left status |
| `Space l` | issue focused | move issue right status |

## 9.13 Detail Panel Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `Ctrl-u` | detail open | scroll up |
| `Ctrl-d` | detail open | scroll down |
| `j` | attachments exist | next attachment selected |
| `k` | attachments exist | previous attachment selected/deselect |
| `v` | attachment selected | preview overlay opens |
| `o` | attachment selected | external viewer opens |
| `x` | attachment selected | attachment deleted |
| `i` | detail open | attachment add overlay opens |
| `g` | epic detail open | enter epic drill-down |
| `m` | issue detail with upstream source selected | invoke follow-on merge from upstream source into current issue |
| `Enter` | detail open | close detail panel |
| `Esc` | detail open | close detail panel |

## 9.14 Attachment Overlay Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `p` | overlay open | attempt paste from clipboard |
| `v` | overlay open | alias for paste from clipboard |
| `f` | overlay open | enter path input mode |
| `Esc` | overlay open | close or step back |

### Path Input Mode

| Key | Postconditions |
|---|---|
| printable | append path text |
| `Backspace` | remove char |
| `Enter` | validate and attach file |
| `Esc` | return to overlay menu |

## 9.15 Planning Overlay Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| printable | input phase | append prompt text |
| `Backspace` | input phase | delete char |
| `Enter` | input phase | submit planning request |
| `Esc` | input/generation | cancel/close where safe |
| `a` | generation/review | attach to planning session |
| `q` | completion phase | close overlay |

## 9.16 Settings Overlay Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `j` | settings open | move selection down |
| `k` | settings open | move selection up |
| `Space` | toggleable setting | toggle/cycle value |
| `Enter` | toggleable setting | toggle/cycle value |
| `e` | settings open | open raw config editor |
| `Esc` | settings open | close overlay |

## 9.17 Project Selector Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `1`..`9` | selector open, index exists | switch to chosen project |
| `Esc` | selector open | close without switching |

## 9.18 Logs Menu Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `v` | logs menu open | view logs |
| `p` | logs menu open | open operations monitor |
| `e` | logs menu open | edit/open logs config/file |
| `q` | logs menu open | close logs menu |

### Operations Monitor Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `j/k` | operations monitor open | move operation selection |
| `Enter` | operation selected | open operation detail |
| `c` | cancellable operation selected | request cancel for selected operation |
| `q` | operations monitor open | close operations monitor |

## 9.19 Drill-Down Keys

| Key | Preconditions | Postconditions |
|---|---|---|
| `h/j/k/l` | drill-down active | navigate within child board |
| `Enter` | child issue focused | open detail panel |
| `Space m` | parent drill-down, child issue focused | merge from selected/preselected parent-upstream source into focused child |
| `Space F` | parent drill-down, child creation/fork intent | fork child with runtime origin choice (base branch or parent/upstream branch) |
| `q` | drill-down active | exit drill-down |
| `Esc` | drill-down active | exit drill-down |

## 9.20 Keybinding Conflict Rules

- If same key exists in multiple contexts, focused context wins.
- Overlay-local keys supersede board mode keys.
- `Esc` always acts as nearest-scope back/close first.

## 9.21 Keybinding Stability Policy

- Key assignments in this section are part of the product contract.
- Changes require explicit versioned migration documentation.
- If a key is deprecated, provide alias period and visible notice.
