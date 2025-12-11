/**
 * @since 1.0.0
 * SolidJS hooks for effect-atom
 */
import type * as Atom from "@effect-atom/atom/Atom"
import type * as Registry from "@effect-atom/atom/Registry"
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
 *
 * @since 1.0.0
 * @category hooks
 */
export function useAtomSet<R, W>(atom: Atom.Writable<R, W>): (value: W | ((current: R) => W)) => void {
  const registry = useRegistry()

  return (value: W | ((current: R) => W)) => {
    if (typeof value === "function") {
      const currentValue = registry.get(atom)
      const newValue = (value as (current: R) => W)(currentValue)
      registry.set(atom, newValue)
    } else {
      registry.set(atom, value)
    }
  }
}

/**
 * Combined hook returning [Accessor, Setter] tuple.
 *
 * @since 1.0.0
 * @category hooks
 */
export function useAtom<R, W>(
  atom: Atom.Writable<R, W>
): [Accessor<R>, (value: W | ((current: R) => W)) => void] {
  return [useAtomValue(atom), useAtomSet(atom)]
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
