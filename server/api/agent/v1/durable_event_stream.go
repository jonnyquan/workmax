package agentv1api

import (
	"context"
	"fmt"

	"server/service/agentturn"
)

// durableEventStream adapts the service-layer replay-to-live stream to this
// package's EventStream boundary.
//
// The adapter exists only because Go does not treat a concrete return type as
// satisfying an interface-returning method. It adds no behaviour: ownership,
// cursor validation, ordering, terminal detection and detach semantics all
// stay in the service layer, so the handler cannot acquire a second opinion
// about any of them.
type durableEventStream struct {
	stream *agentturn.TurnEventStream
}

// NewDurableEventStream wires the durable Turn event stream into the candidate
// handler. Composing it still requires a caller that mounts this package, and
// no production router does.
func NewDurableEventStream(stream *agentturn.TurnEventStream) (EventStream, error) {
	if stream == nil {
		return nil, fmt.Errorf("agent v1 durable event stream requires a turn event stream")
	}
	return &durableEventStream{stream: stream}, nil
}

func (adapter *durableEventStream) Subscribe(ctx context.Context, command agentturn.AttachCommand) (EventSubscription, error) {
	subscription, err := adapter.stream.Subscribe(ctx, command)
	if err != nil {
		return nil, err
	}
	if subscription == nil {
		return nil, fmt.Errorf("durable turn stream returned a nil subscription")
	}
	return subscription, nil
}
