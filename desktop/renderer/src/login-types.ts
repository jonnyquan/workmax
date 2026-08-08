// The renderer-visible half of the login transaction vocabulary.
//
// These types used to live in the Electron main-process module that drove the
// flow. The renderer never needed the rest of that module — only the shapes it
// sends and receives — so on the Wails shell they live here, next to the
// bridge that uses them, and the privileged half stays in Go.
//
// Deliberately narrow: the renderer learns a state and, at most, a coarse
// error code. It never sees the flow ID, and never learns anything that would
// let it distinguish "wrong password" from "no such account" beyond what the
// server chooses to say.

export type LoginTransactionState =
  | "idle"
  | "awaiting_password"
  | "submitting"
  | "authenticated";

export type LoginTransactionPublicError =
  | "busy"
  | "invalid_request"
  | "invalid_credentials"
  | "expired"
  | "unavailable"
  | "canceled";

export interface LoginPasswordInput {
  email: string;
  password: string;
}

export type LoginTransactionResult =
  | { state: LoginTransactionState }
  | { state: LoginTransactionState; error: LoginTransactionPublicError };
