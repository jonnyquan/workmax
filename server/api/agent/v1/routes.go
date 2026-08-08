package agentv1api

const (
	StartTurnsPath = "/api/v1/agent/threads/:threadId/turns"
	TurnStatusPath = "/api/v1/agent/threads/:threadId/turns/:turnId"
	TurnStreamPath = "/api/v1/agent/threads/:threadId/turns/:turnId/stream"
	CancelTurnPath = "/api/v1/agent/threads/:threadId/turns/:turnId/cancel"
)

type CandidateRoute struct {
	ID     string
	Method string
	Path   string
}

var candidateRoutes = []CandidateRoute{
	{ID: "agent.turn.start", Method: "POST", Path: StartTurnsPath},
	{ID: "agent.turn.status", Method: "GET", Path: TurnStatusPath},
	{ID: "agent.turn.stream", Method: "GET", Path: TurnStreamPath},
	{ID: "agent.turn.cancel", Method: "POST", Path: CancelTurnPath},
}

// CandidateRoutes returns a defensive copy of the target-only route catalog.
// It is inventory, not a registration function. The production initialize
// router deliberately does not consume it; tests compose a router explicitly.
func CandidateRoutes() []CandidateRoute {
	return append([]CandidateRoute(nil), candidateRoutes...)
}
