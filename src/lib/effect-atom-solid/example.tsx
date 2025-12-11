/**
 * @since 1.0.0
 * Example usage of effect-atom-solid
 */
import * as Atom from "@effect-atom/atom/Atom"
import { type Component } from "solid-js"
import { AtomProvider, useAtomValue, useAtomSet, useAtom } from "./index"

// Create a simple counter atom
const counterAtom = Atom.make(0)

// Example component using useAtomValue
const CounterDisplay: Component = () => {
  const count = useAtomValue(counterAtom)

  return <div>Count: {count()}</div>
}

// Example component using useAtomSet
const CounterControls: Component = () => {
  const setCount = useAtomSet(counterAtom)

  return (
    <div>
      <button onClick={() => setCount((n) => n + 1)}>Increment</button>
      <button onClick={() => setCount((n) => n - 1)}>Decrement</button>
      <button onClick={() => setCount(0)}>Reset</button>
    </div>
  )
}

// Example component using useAtom (combined)
const CounterCombined: Component = () => {
  const [count, setCount] = useAtom(counterAtom)

  return (
    <div>
      <div>Count: {count()}</div>
      <button onClick={() => setCount((n) => n + 1)}>+</button>
      <button onClick={() => setCount((n) => n - 1)}>-</button>
    </div>
  )
}

// Root app component
export const ExampleApp: Component = () => {
  return (
    <AtomProvider>
      <div>
        <h1>Effect Atom SolidJS Example</h1>
        <CounterDisplay />
        <CounterControls />
        <hr />
        <CounterCombined />
      </div>
    </AtomProvider>
  )
}
