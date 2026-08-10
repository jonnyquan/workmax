//go:build desktop

package local_inference

import (
	"errors"

	cloudproxy "server/desktop/cloud_proxy"
)

// ProfileError is a profile-resolution failure that already knows what the
// user should be told.
//
// Reading a profile used to have exactly one interesting outcome — "the local
// model is not configured" — so every failure could share one message. Once a
// local turn can also be pointed at an official model, the failures stop being
// interchangeable: not signed in, no model chosen, and gateway not ready are
// three different situations with three different next actions, and only the
// resolver knows which one happened.
//
// The alternative (an untyped error whose text the engine wraps in a generic
// message) was tried in the first draft and reads badly: the actionable
// sentence ends up buried in a details map while the headline says something
// vague about configuration. So the resolver states kind and message, and the
// engines pass them through unchanged.
type ProfileError struct {
	Kind      cloudproxy.ProxyErrorKind
	Message   string
	Retryable bool
}

func (e *ProfileError) Error() string { return e.Message }

// ProfileProxyError extracts the typed proxy error a resolver attached, if it
// attached one. Engines call it before falling back to their generic
// "could not read the local model settings" message.
func ProfileProxyError(err error) (cloudproxy.ProxyError, bool) {
	var profileErr *ProfileError
	if !errors.As(err, &profileErr) {
		return cloudproxy.ProxyError{}, false
	}
	return cloudproxy.ProxyError{
		Kind:      profileErr.Kind,
		Message:   profileErr.Message,
		Retryable: profileErr.Retryable,
	}, true
}
