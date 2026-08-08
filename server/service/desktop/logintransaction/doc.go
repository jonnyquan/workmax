// Package logintransaction owns the short-lived authentication transaction
// that bridges a native Desktop login to the existing OAuth authorization-code
// flow.
//
// The package deliberately has no dependency on Gin, GORM, global
// configuration, JWT cookies, or the legacy Portal auth handlers. Callers
// provide identity authenticators and a repository. The in-memory repository
// is suitable for tests and single-process development only; production must
// provide a shared, transactional implementation.
package logintransaction
