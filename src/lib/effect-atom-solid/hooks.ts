/**
 * @since 1.0.0
 * SolidJS hooks for effect-atom
 */
import type * as Atom from "@effect-atom/atom/Atom"
import type * as Registry from "@effect-atom/atom/Registry"
import type { Result } from "@effect-atom/atom"
import { createContext, useContext, createSignal, onCleanup, type Accessor } from "solid-js"

/**
 * @since 1.0.0
 * @category context
 */
export const RegistryContext = createContext<Registry.Registry>()

/**
 * @since 1.0.0
 * @category hooks
 */
export function useRegistry(): Registry.Registry {
  const registry = useContext(RegistryContext)
  if (!registry) {
    throw new Error("RegistryContext not provided. Wrap your app with <AtomProvider>")
  }
  return registry
}

/**
 * Subscribe to an atom and return a reactive accessor.
 *
 * @since 1.0.0
 * @category hooks
 */
export function useAtomValue<A>(atom: Atom.Atom<A>): Accessor<A>
export function useAtomValue<A, B>(atom: Atom.Atom<A>, f: (value: A) => B): Accessor<B>
export function useAtomValue<A, B>(atom: Atom.Atom<A>, f?: (value: A) => B): Accessor<A | B> {
  const registry = useRegistry()

  // Get initial value
  const initialValue = registry.get(atom)

  // Create signal
  const [value, setValue] = createSignal<A>(initialValue, { equals: false })

  // Subscribe to atom changes
  const unsubscribe = registry.subscribe(atom, (newValue: A) => {
    setValue(() => newValue)
  })

  // Clean up subscription on component cleanup
  onCleanup(unsubscribe)

  // If transform function provided, create derived accessor
  if (f) {
    return () => f(value())
  }

  return value as Accessor<A | B>
}

/**
 * Return a setter function for writable atoms.
 * Supports both direct values and function updaters.
 *
 * @since 1.0.0
 * @category hooks
 */
export function useAtomSet<R, W = R>(atom: Atom.Writable<R, W>): (value: W | ((current: R) => W)) => void

/**
 * Return an async setter function for fn atoms (created with runtime.fn).
 *
 * @since 1.0.0
 * @category hooks
 *
 * @example
 * ```tsx
 * const startSession = useAtomSet(startSessionAtom, { mode: "promise" })
 * await startSession(taskId)
 * ```
 */
export function useAtomSet<R, W>(
  atom: Atom.Writable<R, W>,
  options: { mode: "promise" }
): (value: W) => Promise<R extends Result.Result<infer A, infer _E> ? A : R>

/**
 * Implementation
 */
export function useAtomSet<R, W>(
  atom: Atom.Writable<R, W>,
  options?: { mode?: "promise" | "promiseExit" }
): (value: W | ((current: R) => W)) => void | Promise<unknown> {
  const registry = useRegistry()

  if (options?.mode === "promise" || options?.mode === "promiseExit") {
    // For fn atoms, we need to set and then wait for the result
    return (value: W | ((current: R) => W)): Promise<unknown> => {
      return new Promise((resolve, reject) => {
        // Set the value to trigger the effect
        if (typeof value === "function") {
          registry.update(atom, value as (current: R) => W)
        } else {
          registry.set(atom, value)
        }

        // Subscribe to get the result
        const unsubscribe = registry.subscribe(atom, (result: unknown) => {
          // Check if it's a Result type (from fn atoms)
          if (result && typeof result === "object" && "_tag" in result) {
            const r = result as { _tag: string; value?: unknown; cause?: unknown }
            if (r._tag === "Success") {
              unsubscribe()
              resolve(r.value)
            } else if (r._tag === "Failure") {
              unsubscribe()
              if (options.mode === "promiseExit") {
                resolve(result)
              } else {
                reject(r.cause)
              }
            }
            // If initial/waiting, keep waiting
          } else {
            // Not a Result, resolve immediately
            unsubscribe()
            resolve(result)
          }
        })
      })
    }
  }

  // Default synchronous mode - supports function updaters
  return (value: W | ((current: R) => W)): void => {
    if (typeof value === "function") {
      registry.update(atom, value as (current: R) => W)
    } else {
      registry.set(atom, value)
    }
  }
}

/**
 * Mount an atom for its lifecycle (runs effects, manages resources).
 *
 * @since 1.0.0
 * @category hooks
 */
export function useAtomMount<A>(atom: Atom.Atom<A>): void {
  const registry = useRegistry()

  // Mount the atom
  const unmount = registry.mount(atom)

  // Unmount on cleanup
  onCleanup(unmount)
}

/**
 * Return a function to refresh an atom's value.
 *
 * @since 1.0.0
 * @category hooks
 */
export function useAtomRefresh<A>(atom: Atom.Atom<A>): () => void {
  const registry = useRegistry()

  return () => {
    registry.refresh(atom)
  }
}
