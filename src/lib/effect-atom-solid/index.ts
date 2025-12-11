/**
 * @since 1.0.0
 * SolidJS adapter for @effect-atom/atom
 *
 * A reactive state management library for Effect, ported to SolidJS.
 * Similar to @effect-atom/atom-react but uses SolidJS's reactive primitives.
 */

// Re-export hooks
export {
  RegistryContext,
  useRegistry,
  useAtomValue,
  useAtomSet,
  useAtom,
  useAtomMount,
  useAtomRefresh
} from "./hooks"

// Re-export Provider
export { AtomProvider } from "./Provider"

// Re-export core types from effect-atom
export type { Atom, Writable } from "@effect-atom/atom/Atom"
export type { Registry } from "@effect-atom/atom/Registry"
