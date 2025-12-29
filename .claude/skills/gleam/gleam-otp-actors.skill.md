<!--
Skill: Gleam OTP Actors
Triggers: gleam actor, otp actor, process.new_subject, actor message, gleam timer, actor self
Version: 1.0.0
-->

# Gleam OTP Actor Patterns

Essential patterns for building correct, message-passing actors in Gleam using `gleam_otp`.

## Core Concept: Subjects Are Mailboxes

In Gleam OTP, a `Subject(Msg)` is a **typed mailbox**. Messages sent to a subject are received by whoever is blocking on `process.receive(subject, timeout)`.

**Critical insight:** `process.new_subject()` creates a **brand new, independent mailbox** that is NOT connected to any actor's message handler. If nobody calls `receive` on it, messages are silently discarded.

## The Orphan Subject Antipattern

```gleam
// ❌ WRONG: Orphan subject - nobody listens!
fn handle_message(state, msg) {
  case msg {
    DoAsyncWork -> {
      let reply_to = process.new_subject()  // Creates orphan!
      spawn_worker(reply_to)  // Worker sends result to orphan
      actor.continue(state)   // Result is LOST
    }
  }
}
```

The spawned worker sends its result to `reply_to`, but:
- The actor's handler doesn't receive on that subject
- Nobody else is listening
- The message vanishes into the void

**Symptoms:**
- No errors or warnings (silent failure)
- State never updates after async operations
- UI doesn't refresh after CRUD operations
- Timers/polls don't trigger handlers

## Pattern 1: Actor Self-Reference

Actors don't have built-in access to their own subject. Pass it explicitly:

```gleam
// Define message with self parameter
pub type Msg {
  Initialize(self: Subject(Msg))
  PeriodicTick
  WorkerResult(data: String)
}

// Caller passes subject when initializing
pub fn initialize(subject: Subject(Msg)) -> Nil {
  process.send(subject, Initialize(subject))
}

// Handler stores and uses self reference
fn handle_message(state, msg) {
  case msg {
    Initialize(self) -> {
      // Store self for later use
      schedule_tick(self)
      actor.continue(State(..state, self_subject: Some(self)))
    }

    PeriodicTick -> {
      do_periodic_work()
      // Use stored self to schedule next tick
      case state.self_subject {
        Some(self) -> schedule_tick(self)
        None -> Nil
      }
      actor.continue(state)
    }
  }
}

// Timer sends back to actor's real subject
fn schedule_tick(self: Subject(Msg)) -> Nil {
  let _ = process.spawn(fn() {
    process.sleep(1000)
    process.send(self, PeriodicTick)  // Goes to actor's handler!
  })
  Nil
}
```

## Pattern 2: Async Work with Reply

For spawning async work that returns results to the actor:

```gleam
pub type Msg {
  StartWork(id: String)
  WorkComplete(id: String, result: Result(Data, Error))
}

fn handle_message(state, msg) {
  case msg {
    StartWork(id) -> {
      // Use self_subject to receive result
      case state.self_subject {
        Some(self) -> spawn_async_worker(self, id)
        None -> Nil  // Not initialized, skip
      }
      actor.continue(state)
    }

    WorkComplete(id, result) -> {
      // Process result and update state
      let new_state = apply_result(state, id, result)
      actor.continue(new_state)
    }
  }
}

fn spawn_async_worker(reply_to: Subject(Msg), id: String) -> Nil {
  let _ = process.spawn(fn() {
    let result = do_expensive_work(id)
    process.send(reply_to, WorkComplete(id, result))
  })
  Nil
}
```

## Pattern 3: Bridge for Message Type Conversion

When forwarding messages between actors with different message types:

```gleam
// Source actor sends: SourceMsg
// Target actor expects: TargetMsg

/// Create a bridge that converts SourceMsg -> TargetMsg
fn create_bridge(
  target: Subject(TargetMsg),
) -> Subject(SourceMsg) {
  let bridge_subject = process.new_subject()

  // Spawn process that listens on bridge and forwards to target
  let _ = process.spawn(fn() {
    forward_loop(bridge_subject, target)
  })

  bridge_subject  // Give this to the source actor
}

fn forward_loop(
  from: Subject(SourceMsg),
  to: Subject(TargetMsg),
) -> Nil {
  case process.receive(from, 60_000) {
    Ok(SourceEvent(id, data)) -> {
      // Convert and forward
      process.send(to, TargetEvent(id, data))
      forward_loop(from, to)  // Continue listening
    }
    Ok(SourceError(id, reason)) -> {
      process.send(to, TargetError(id, reason))
      forward_loop(from, to)
    }
    Error(_) -> {
      // Timeout - continue listening
      forward_loop(from, to)
    }
  }
}
```

**Key insight:** The bridge subject is NOT orphan because we spawn a process that immediately starts receiving on it.

## Pattern 4: Request-Reply (Synchronous Call)

For synchronous calls where caller blocks waiting for response:

```gleam
// In the API module
pub fn get_monitors(supervisor: Subject(Msg)) -> List(String) {
  let reply_subject = process.new_subject()  // OK here!
  process.send(supervisor, ListMonitors(reply_subject))

  // Caller blocks until reply received
  case process.receive(reply_subject, 5000) {
    Ok(monitors) -> monitors
    Error(_) -> []  // Timeout
  }
}

// In the actor's message handler
fn handle_message(state, msg) {
  case msg {
    ListMonitors(reply_to) -> {
      let monitors = dict.keys(state.monitors)
      process.send(reply_to, monitors)  // Reply to waiting caller
      actor.continue(state)
    }
  }
}
```

**Why this works:** The caller immediately calls `process.receive()` on the subject, so someone IS listening. The subject isn't orphaned.

## Pattern 5: Startup Order for Dependencies

When actors depend on each other's subjects, start in dependency order:

```gleam
// ❌ WRONG: Start order doesn't respect dependencies
fn start_children() {
  let callback = process.new_subject()  // Orphan!
  start_worker(callback)  // Worker will send to orphan
  start_coordinator()     // Coordinator should receive, but started too late
}

// ✅ CORRECT: Start receiver first, then sender
fn start_children() {
  // Start coordinator first - it will receive updates
  case coordinator.start() {
    Ok(coord) -> {
      // Create bridge that forwards to coordinator
      let callback = create_bridge(coord)
      // Now start worker with valid callback
      start_worker(callback)
    }
    Error(_) -> // Handle error
  }
}
```

## Common Mistakes Checklist

Before using `process.new_subject()`, verify:

1. **Is someone calling `receive()` on it?**
   - If blocking synchronously: OK
   - If spawning a forwarding loop: OK
   - If just passing around: PROBLEM

2. **For async results, am I using the actor's self?**
   - `process.new_subject()` for results: WRONG
   - `state.self_subject` for results: CORRECT

3. **For timers/schedulers, where do messages go?**
   - `process.send(new_subject, Tick)`: Lost
   - `process.send(self_subject, Tick)`: Handled

4. **For bridges, is there a receiver process?**
   - Create subject + return it: ORPHAN
   - Create subject + spawn receiver + return it: CORRECT

## Debugging Tips

**Symptom:** "Actor discarding unexpected message"
- An Erlang timer or other system is sending to a subject that exists but has no pattern match
- Check if you're creating subjects inside the actor that should be the actor's own subject

**Symptom:** State never updates after async operation
- Results are being sent to orphan subjects
- Add logging in the spawned worker to see if it's sending
- Check if `self_subject` is `None` when spawning

**Symptom:** Periodic tasks stop after first run
- Forgetting to reschedule the next tick
- Using wrong subject for rescheduling

## Quick Reference

```gleam
// Get actor's self: Pass via Initialize message, store in state
// Schedule timer: spawn + sleep + send to self
// Async with reply: spawn + work + send result to self
// Sync request-reply: new_subject + send request + receive reply
// Bridge pattern: new_subject + spawn receiver loop + return subject
// NEVER: new_subject for fire-and-forget results
```
