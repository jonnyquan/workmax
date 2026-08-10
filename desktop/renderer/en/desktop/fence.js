// Fences: the renderer's answer to "this reply arrived, but is it still the
// answer to a question anyone is asking?"
//
// Every asynchronous thing here — a fetch, a stream, an upload, a debounce —
// can land after the user has moved on, and applying a stale result is not a
// cosmetic bug: it is one thread's messages appearing under another thread's
// title, or a cancelled sign-in completing. The renderer has always guarded
// that with monotonic counters. What it did not have was a name for them.
//
// There were eight, each an ad-hoc `+= 1` on a state field, each compared with
// a bare `!==` at the point of use, and the rules connecting them ("changing
// session invalidates everything in flight") existed only in comments and in
// whether every call site remembered to bump its neighbours. This module makes
// the counter an object with three verbs and makes the connections structural.
//
//   const token = fences.session.snapshot();   // before you go away
//   ...
//   if (!fences.session.isCurrent(token)) return;   // after you come back
//
// The one rule worth stating outright: NEVER compare snapshots with `===`
// against another snapshot. isCurrent is the only comparison, because it is
// the only one that stays right if a fence ever grows a second way to advance.

// createFence returns a monotonic guard. `dependents` are the fences that a
// bump of this one also invalidates — see the session fence below, which is
// the only place the renderer has a real hierarchy.
export function createFence(name, dependents = []) {
  let current = 0;
  return {
    name,
    // The value to carry across an await. Deliberately opaque: callers store
    // it and hand it back, and nothing else about it is promised.
    snapshot() {
      return current;
    },
    // Invalidate everything outstanding, and everything outstanding under the
    // fences this one covers. Returns the new snapshot for the caller that
    // wants to start its own work immediately after.
    bump() {
      current += 1;
      for (const dependent of dependents) dependent.bump();
      return current;
    },
    // The only sanctioned comparison.
    isCurrent(token) {
      return token === current;
    },
  };
}

// Which selection the transcript is showing. Bumped by picking a thread, by
// clearing the workbench, and by anything that makes "the messages on screen"
// answer to a different question than the one in flight.
const selection = createFence("selection");

// Which turn the stream belongs to. A cancelled or superseded turn's frames
// must not paint, and its terminal event must not re-enable the composer for
// the turn that replaced it.
const turn = createFence("turn");

// Which new-thread attempt is live. Create is retried with the SAME uuid on
// purpose (that is what makes it idempotent), so "is this the reply to the
// attempt still on screen?" cannot be answered by the uuid and needs a fence.
const create = createFence("create");

// Which recovery offer is live: the interrupted-turn card, its resume and its
// dismiss all race with each other and with a fresh turn.
const recovery = createFence("recovery");

// Which sign-in attempt is live. Login polls, so a cancelled attempt has a
// timer that will come back with an answer nobody wants.
const loginOperation = createFence("loginOperation");

// Which message-content search is live. Debounced typing plus a sidecar
// round-trip means answers can arrive out of order; only the newest may paint.
const contentSearch = createFence("contentSearch");

// The session fence sits above the rest: identity changed, so every question
// asked as the previous identity now has a wrong answer. Bumping it bumps them
// all, which is the rule that used to live in a comment and in the hope that
// every session-change path remembered to bump its four neighbours by hand.
//
// This is a widening, never a narrowing: each of the guards below already
// compared the session snapshot as well, so cascading cannot make a result
// current that was previously stale.
const session = createFence("session", [selection, turn, create, recovery, loginOperation]);

export const fences = {
  session,
  selection,
  turn,
  create,
  recovery,
  loginOperation,
  contentSearch,
};
