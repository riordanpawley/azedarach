/**
 * @since 1.0.0
 * SolidJS Provider component for effect-atom
 */
import type * as Atom from "@effect-atom/atom/Atom"
import * as Registry from "@effect-atom/atom/Registry"
import { type ParentProps, onCleanup, type Component } from "solid-js"
import { RegistryContext } from "./hooks"

/**
 * Provider component for the Atom Registry.
 *
 * @since 1.0.0
 * @category components
 */
export const AtomProvider: Component<
  ParentProps<{
    registry?: Registry.Registry
    initialValues?: Iterable<readonly [Atom.Atom<any>, any]>
    scheduleTask?: (f: () => void) => void
    timeoutResolution?: number
    defaultIdleTTL?: number
  }>
> = (props) => {
  // Create registry if not provided
  // Use lazy evaluation - only create once during component init
  let registry: Registry.Registry

  if (props.registry) {
    registry = props.registry
  } else {
    registry = Registry.make({
      scheduleTask: props.scheduleTask ?? queueMicrotask,
      initialValues: props.initialValues,
      timeoutResolution: props.timeoutResolution,
      defaultIdleTTL: props.defaultIdleTTL ?? 400
    })
  }

  // Clean up registry on component cleanup
  onCleanup(() => {
    // Dispose registry after a delay to allow cleanup
    setTimeout(() => {
      registry.dispose()
    }, 500)
  })

  return (
    <RegistryContext.Provider value={registry}>
      {props.children}
    </RegistryContext.Provider>
  )
}
