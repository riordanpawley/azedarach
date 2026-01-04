/**
 * Atoms for Azedarach UI state
 *
 * This file re-exports all atoms from the atoms/ directory.
 * The atoms are now organized into logical modules for better maintainability.
 *
 * Module breakdown:
 * - atoms/runtime.ts    - appRuntime and layer setup
 * - atoms/board.ts      - Board state and filtering
 * - atoms/clock.ts      - Time-based state for elapsed timers
 * - atoms/commandQueue.ts - Action busy state tracking
 * - atoms/diagnostics.ts - System health monitoring
 * - atoms/image.ts      - Image attachment management
 * - atoms/keyboard.ts   - Keyboard input handling
 * - atoms/mode.ts       - Editor mode state (normal, select, search, etc.)
 * - atoms/navigation.ts - Cursor navigation and position
 * - atoms/overlay.ts    - Overlay stack and toast notifications
 * - atoms/pr.ts         - PR creation and merge operations
 * - atoms/project.ts    - Project selection and management
 * - atoms/session.ts    - Claude session lifecycle
 * - atoms/task.ts       - Task CRUD operations
 * - atoms/vc.ts         - VC auto-pilot status and control
 */

export * from "./atoms/index.js"
