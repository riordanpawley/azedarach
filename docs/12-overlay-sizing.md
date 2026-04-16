# Overlay Sizing Contract

Use this as the canonical reference for overlay geometry and review checks.

## Contract

- `View()` renders with the dimensions returned by `Size()`.
- `Size()` owns the responsive policy.
- Standard overlays should use a clamp-based responsive size helper.
- Fullscreen or other special-case overlays are allowed, but the exception must be explicit in code and docs.

## Behavior Change Workflow

- Before changing overlay behavior, check the active issue's `az spec` requirements/links and align those first.
- If the work is docs/process-only, note `Spec impact: none (docs/process-only)` in the issue notes.
- If rendered output changes, update the default and small-viewport goldens in the same change.

## Canonical Pattern

```go
func (m Model) Size() (int, int) {
    return ClampResponsive(64, 12)
}

func (m Model) View() string {
    width, height := m.Size()
    return renderDialog(width, height, m.title, m.body, m.actions)
}
```

If an overlay is intentionally fullscreen, make that obvious:

```go
func (m Model) Size() (int, int) {
    return m.width, m.height
}
```

## Validation Expectations

- Standard/default viewport: the overlay is centered or anchored as designed, body copy is readable, and controls are not clipped.
- Narrow viewport: the overlay still fits the terminal bounds, content reflows or truncates intentionally, and the dismiss/confirm path remains visible.
- Fullscreen overlay: the exception is documented, and the content still respects the current terminal size.

## Review Checklist

- `View()` does not compute terminal geometry directly.
- `Size()` contains the only responsive sizing decision.
- The overlay has a default-size golden or snapshot.
- The overlay has a small-viewport golden or snapshot.
- Any fullscreen behavior is called out as an explicit exception.
- The overlay does not duplicate sizing math in both `View()` and `Size()`.

## Anti-Patterns

- Hardcoding dialog widths in `View()`.
- Using `m.width` or `m.height` in `View()` to recompute layout.
- Duplicating responsive math in both `View()` and `Size()`.
- Shipping a standard overlay without a small-viewport test case.
- Letting the overlay overflow on narrow terminals without documenting a fullscreen exception.
