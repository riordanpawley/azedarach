# effect-atom-solid

SolidJS adapter for [@effect-atom/atom](https://github.com/tim-smart/effect-atom) - a reactive state management library for Effect.

## Overview

This adapter provides SolidJS bindings for effect-atom, similar to `@effect-atom/atom-react` but using SolidJS's reactive primitives (`createSignal` and `onCleanup`).

## Installation

This adapter is bundled within the Azedarach project. The dependencies are:

- `@effect-atom/atom` - Core atom library
- `solid-js` - SolidJS framework
- `effect` - Effect library

## Usage

### Setup

Wrap your app with the `AtomProvider`:

```tsx
import { AtomProvider } from "./lib/effect-atom-solid"

function App() {
  return (
    <AtomProvider>
      <YourApp />
    </AtomProvider>
  )
}
```

### Creating Atoms

```typescript
import * as Atom from "@effect-atom/atom/Atom"

// Simple writable atom
const counterAtom = Atom.make(0)

// Derived atom
const doubledAtom = Atom.make((get) => get(counterAtom) * 2)
```

### Using Atoms in Components

#### `useAtomValue` - Read atom values

```tsx
import { useAtomValue } from "./lib/effect-atom-solid"

function Counter() {
  const count = useAtomValue(counterAtom)

  return <div>Count: {count()}</div>
}
```

#### `useAtomSet` - Update atom values

```tsx
import { useAtomSet } from "./lib/effect-atom-solid"

function CounterControls() {
  const setCount = useAtomSet(counterAtom)

  return (
    <div>
      <button onClick={() => setCount((n) => n + 1)}>+</button>
      <button onClick={() => setCount((n) => n - 1)}>-</button>
      <button onClick={() => setCount(0)}>Reset</button>
    </div>
  )
}
```

#### `useAtom` - Combined read/write

```tsx
import { useAtom } from "./lib/effect-atom-solid"

function Counter() {
  const [count, setCount] = useAtom(counterAtom)

  return (
    <div>
      <div>Count: {count()}</div>
      <button onClick={() => setCount((n) => n + 1)}>+</button>
    </div>
  )
}
```

#### `useAtomMount` - Mount atoms with effects

```tsx
import { useAtomMount } from "./lib/effect-atom-solid"

function Component() {
  // Mount an atom that runs effects
  useAtomMount(someEffectfulAtom)

  return <div>...</div>
}
```

#### `useAtomRefresh` - Manually refresh atoms

```tsx
import { useAtomRefresh } from "./lib/effect-atom-solid"

function Component() {
  const refresh = useAtomRefresh(dataAtom)

  return <button onClick={refresh}>Refresh Data</button>
}
```

## API Reference

### Components

- **`AtomProvider`** - Context provider for the atom registry

### Hooks

- **`useRegistry()`** - Get the current registry instance
- **`useAtomValue<A>(atom: Atom<A>): Accessor<A>`** - Subscribe to atom value
- **`useAtomSet<R, W>(atom: Writable<R, W>): (value: W | ((current: R) => W)) => void`** - Get setter for atom
- **`useAtom<R, W>(atom: Writable<R, W>): [Accessor<R>, Setter]`** - Combined hook
- **`useAtomMount<A>(atom: Atom<A>): void`** - Mount atom for lifecycle
- **`useAtomRefresh<A>(atom: Atom<A>): () => void`** - Get refresh function

## Differences from React Adapter

1. **No `useSyncExternalStore`** - SolidJS uses `createSignal` + subscription instead
2. **Accessors instead of direct values** - Values are returned as `Accessor<T>` and must be called: `count()`
3. **No Suspense hooks** - SolidJS has different patterns for async data
4. **Simpler cleanup** - Uses SolidJS's `onCleanup` primitive

## Type Safety

All hooks are fully typed with TypeScript strict mode. The adapter preserves:

- Atom read/write types
- Writable vs readonly distinctions
- Transform function types

## Example

See `example.tsx` for a complete working example.

## License

MIT

## References

- [effect-atom GitHub](https://github.com/tim-smart/effect-atom)
- [SolidJS Docs](https://docs.solidjs.com)
- [Effect Docs](https://effect.website)
