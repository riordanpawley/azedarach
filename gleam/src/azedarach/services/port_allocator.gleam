// Port Allocator Service
// Manages port allocation pool with conflict resolution
// Matches TypeScript allocatedPortsRef logic

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/set.{type Set}

/// Port allocator state
pub type AllocatorState {
  AllocatorState(
    allocated: Set(Int),
    // Track which bead:server owns which port for release
    ownership: Dict(String, Int),
  )
}

/// Messages for port allocator
pub type Msg {
  /// Allocate a port starting from base, returns actual port
  Allocate(
    base_port: Int,
    owner_key: String,
    reply_to: Subject(Int),
  )
  /// Release a port by owner key
  Release(owner_key: String)
  /// Get allocated port for owner
  GetPort(owner_key: String, reply_to: Subject(Option(Int)))
  /// Check if port is allocated
  IsAllocated(port: Int, reply_to: Subject(Bool))
  /// Get all allocated ports
  ListAllocated(reply_to: Subject(List(Int)))
  /// Initialize from existing state (e.g., from tmux discovery)
  BulkInit(ports: List(#(String, Int)))
}

/// Start the port allocator actor
pub fn start() -> Result(Subject(Msg), actor.StartError) {
  actor.start_spec(actor.Spec(
    init: fn() {
      let state =
        AllocatorState(
          allocated: set.new(),
          ownership: dict.new(),
        )
      actor.Ready(state, process.new_selector())
    },
    init_timeout: 5000,
    loop: handle_message,
  ))
}

/// Allocate a port for a bead:server
/// Returns the allocated port (may be different from base if conflict)
pub fn allocate(
  allocator: Subject(Msg),
  base_port: Int,
  owner_key: String,
) -> Int {
  let reply_to = process.new_subject()
  process.send(allocator, Allocate(base_port, owner_key, reply_to))
  case process.receive(reply_to, 5000) {
    Ok(port) -> port
    Error(_) -> base_port
  }
}

/// Release a port by owner key
pub fn release(allocator: Subject(Msg), owner_key: String) -> Nil {
  process.send(allocator, Release(owner_key))
}

/// Get the port for an owner
pub fn get_port(allocator: Subject(Msg), owner_key: String) -> Option(Int) {
  let reply_to = process.new_subject()
  process.send(allocator, GetPort(owner_key, reply_to))
  case process.receive(reply_to, 5000) {
    Ok(port) -> port
    Error(_) -> None
  }
}

/// Check if a port is allocated
pub fn is_allocated(allocator: Subject(Msg), port: Int) -> Bool {
  let reply_to = process.new_subject()
  process.send(allocator, IsAllocated(port, reply_to))
  case process.receive(reply_to, 5000) {
    Ok(allocated) -> allocated
    Error(_) -> False
  }
}

/// Initialize with existing ports from discovery
pub fn bulk_init(allocator: Subject(Msg), ports: List(#(String, Int))) -> Nil {
  process.send(allocator, BulkInit(ports))
}

/// Main message handler
fn handle_message(
  msg: Msg,
  state: AllocatorState,
) -> actor.Next(Msg, AllocatorState) {
  case msg {
    Allocate(base_port, owner_key, reply_to) -> {
      // Check if owner already has a port
      case dict.get(state.ownership, owner_key) {
        Ok(existing_port) -> {
          process.send(reply_to, existing_port)
          actor.continue(state)
        }
        Error(_) -> {
          // Find next available port
          let port = find_available_port(state.allocated, base_port)
          let new_allocated = set.insert(state.allocated, port)
          let new_ownership = dict.insert(state.ownership, owner_key, port)
          process.send(reply_to, port)
          actor.continue(AllocatorState(
            allocated: new_allocated,
            ownership: new_ownership,
          ))
        }
      }
    }

    Release(owner_key) -> {
      case dict.get(state.ownership, owner_key) {
        Ok(port) -> {
          let new_allocated = set.delete(state.allocated, port)
          let new_ownership = dict.delete(state.ownership, owner_key)
          actor.continue(AllocatorState(
            allocated: new_allocated,
            ownership: new_ownership,
          ))
        }
        Error(_) -> actor.continue(state)
      }
    }

    GetPort(owner_key, reply_to) -> {
      let port = case dict.get(state.ownership, owner_key) {
        Ok(p) -> Some(p)
        Error(_) -> None
      }
      process.send(reply_to, port)
      actor.continue(state)
    }

    IsAllocated(port, reply_to) -> {
      process.send(reply_to, set.contains(state.allocated, port))
      actor.continue(state)
    }

    ListAllocated(reply_to) -> {
      process.send(reply_to, set.to_list(state.allocated))
      actor.continue(state)
    }

    BulkInit(ports) -> {
      let new_state =
        list.fold(ports, state, fn(acc, pair) {
          let #(owner_key, port) = pair
          AllocatorState(
            allocated: set.insert(acc.allocated, port),
            ownership: dict.insert(acc.ownership, owner_key, port),
          )
        })
      actor.continue(new_state)
    }
  }
}

/// Find next available port starting from base
fn find_available_port(allocated: Set(Int), base: Int) -> Int {
  case set.contains(allocated, base) {
    False -> base
    True -> find_available_port(allocated, base + 1)
  }
}

/// Make owner key from bead_id and server_name
pub fn make_owner_key(bead_id: String, server_name: String) -> String {
  bead_id <> ":" <> server_name
}
