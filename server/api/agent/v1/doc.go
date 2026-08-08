// Package agentv1api contains the unmounted candidate HTTP adapter for the
// durable Agent v1 contract.
//
// No production router imports this package. The adapter deliberately depends
// on an authenticated Principal resolver, a server-side Start resolver, a
// durable Turn runtime and an atomic replay/live subscription provider. The
// current MemoryStore cannot satisfy the production readiness requirements.
package agentv1api
