package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"server/config"
	"server/globals"
	"server/middleware"
	"server/model"
)

// rateLimitOp is the bucket name the shared per-user limiter keys on. One
// bucket for both protocols: the resource being protected is platform spend,
// and a user must not double their budget by alternating routes.
const rateLimitOp = "desktop_model_gateway"

// GatewayRequestIDHeader carries our own request id back to the client. It is
// how a support report joins to a w_desktop_model_gateway_usage row without
// anyone handling a token or a provider identifier.
const GatewayRequestIDHeader = "X-WorkMax-Gateway-Request-Id"

// Gateway is the whole cloud-side surface. One instance is built at router
// init and shared; per-request state lives on the stack.
type Gateway struct {
	db       *gorm.DB
	cfg      *config.ModelGateway
	resolver *ModelResolver
	accounts ProviderAccountSource
	upstream *Upstream
	usage    UsageRecorder
	limiter  *middleware.UserRateLimitRegistry
}

// Options lets tests substitute the two collaborators with real behaviour but
// no external dependencies: a fake account source pointing at an httptest
// server, and an in-memory usage recorder.
type Options struct {
	Accounts ProviderAccountSource
	Usage    UsageRecorder
}

// New builds a Gateway. A nil cfg is the default-on configuration, so a
// deployment that never writes a model_gateway block still gets every guard.
func New(db *gorm.DB, cfg *config.ModelGateway, opts Options) *Gateway {
	bucket := cfg.EffectiveRateLimit()
	limiter := middleware.NewUserRateLimitRegistry(
		bucket.GlobalConcurrent,
		func(string) config.CanvasRateLimitBucket {
			return config.CanvasRateLimitBucket{
				PerMinute:         bucket.PerMinute,
				PerUserConcurrent: bucket.PerUserConcurrent,
			}
		},
	)

	accounts := opts.Accounts
	if accounts == nil {
		accounts = NewPoolAccountSource()
	}
	usage := opts.Usage
	if usage == nil {
		usage = NewDBUsageRecorder(db)
	}

	return &Gateway{
		db:       db,
		cfg:      cfg,
		resolver: NewModelResolver(db),
		accounts: accounts,
		upstream: NewUpstream(time.Duration(cfg.EffectiveResponseHeaderTimeoutSeconds()) * time.Second),
		usage:    usage,
		limiter:  limiter,
	}
}

// requestEnvelope is the only part of the caller's body the gateway reads.
// Everything else is forwarded untouched — the gateway is a credential shim,
// not a request rewriter, and inventing opinions about sampling parameters or
// tool definitions here would break clients on the provider's next release.
type requestEnvelope struct {
	Model  string `json:"model"`
	Stream *bool  `json:"stream"`
}

// Handle serves one gateway call end to end. w must be the raw response
// writer (gin's satisfies http.Flusher), because streaming correctness
// depends on flushing frames as they arrive rather than at the end.
func (g *Gateway) Handle(w http.ResponseWriter, r *http.Request, protocol Protocol, uid uint) {
	requestID := uuid.NewString()
	w.Header().Set(GatewayRequestIDHeader, requestID)
	startedAt := time.Now()

	record := UsageRecord{
		UID:       uid,
		RequestID: requestID,
		Protocol:  protocol,
		Stream:    false,
		Status:    model.DesktopModelGatewayUsageStatusFailed,
		StartedAt: startedAt,
	}
	// Every exit path — refusal, upstream failure, success — leaves a row.
	// A call that spent nothing still records WHY, which is what makes a
	// "the gateway is broken" report answerable.
	defer func() {
		record.Duration = time.Since(startedAt)
		g.usage.Record(record)
	}()

	if !g.cfg.IsEnabled() {
		g.fail(w, protocol, &record, newError(http.StatusServiceUnavailable, ErrClassGatewayDisabled,
			"the model gateway is not enabled on this server"))
		return
	}
	if uid == 0 {
		g.fail(w, protocol, &record, newError(http.StatusUnauthorized, ErrClassUnauthorized, "unauthorized"))
		return
	}

	release, retryAfter, allowed, reason := g.limiter.Acquire(uid, rateLimitOp)
	if !allowed {
		gwErr := newError(http.StatusTooManyRequests, ErrClassRateLimited, rateLimitMessage(reason))
		gwErr.RetryAfterSeconds = retryAfter
		g.fail(w, protocol, &record, gwErr)
		return
	}
	defer release()

	body, gwErr := g.readBody(w, r)
	if gwErr != nil {
		g.fail(w, protocol, &record, gwErr)
		return
	}

	var envelope requestEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		g.fail(w, protocol, &record, newError(http.StatusBadRequest, ErrClassInvalidRequest,
			"request body must be a JSON object"))
		return
	}
	record.ModelID = strings.TrimSpace(envelope.Model)
	record.Stream = envelope.Stream != nil && *envelope.Stream

	resolved, gwErr := g.resolver.Resolve(g.dbFor(r.Context()), uid, envelope.Model)
	if gwErr != nil {
		g.fail(w, protocol, &record, gwErr)
		return
	}
	record.UpstreamModel = resolved.UpstreamModel

	account, err := g.accounts.AccountForModel(resolved.ModelID, protocol)
	if err != nil {
		globals.Warn("[ModelGateway] no provider account for model " +
			resolved.ModelID + " protocol " + string(protocol) + ": " + err.Error())
		g.fail(w, protocol, &record, newError(http.StatusServiceUnavailable, ErrClassProviderUnavailable,
			"no upstream capacity is available for this model right now"))
		return
	}
	record.ProviderAccountID = account.ID

	outbound, gwErr := rewriteModel(body, resolved.UpstreamModel)
	if gwErr != nil {
		g.fail(w, protocol, &record, gwErr)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(),
		time.Duration(g.cfg.EffectiveRequestTimeoutSeconds())*time.Second)
	defer cancel()

	resp, err := g.upstream.Do(ctx, protocol, account, outbound, r.Header)
	if err != nil {
		g.accounts.RecordFailure(account.ID, err)
		g.fail(w, protocol, &record, classifyTransportError(ctx, err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status, class, message := classifyUpstreamStatus(resp.StatusCode)
		// The upstream body is drained and discarded, never forwarded: a
		// provider's 401/429 body routinely names the account or key prefix
		// it rejected, and that is exactly what must not cross this boundary.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBufferedResponseBytes))
		g.accounts.RecordFailure(account.ID, &GatewayError{Status: status, Class: class, Message: message})

		gwErr := newError(status, class, message)
		if status == http.StatusTooManyRequests {
			gwErr.RetryAfterSeconds = numericRetryAfter(resp.Header.Get("Retry-After"))
		}
		g.fail(w, protocol, &record, gwErr)
		return
	}

	g.accounts.RecordSuccess(account.ID)
	g.relay(w, resp, protocol, &record)
}

// dbFor scopes the shared handle to the request so a client disconnect
// cancels catalog reads instead of leaving them running.
func (g *Gateway) dbFor(ctx context.Context) *gorm.DB {
	if g.db == nil {
		return nil
	}
	return g.db.WithContext(ctx)
}

// readBody enforces the size cap. http.MaxBytesReader is what makes the limit
// real: it stops reading at the boundary rather than letting us discover the
// overrun after buffering the whole body.
func (g *Gateway) readBody(w http.ResponseWriter, r *http.Request) ([]byte, *GatewayError) {
	if r.Body == nil {
		return nil, newError(http.StatusBadRequest, ErrClassInvalidRequest, "request body is required")
	}
	limit := g.cfg.EffectiveMaxRequestBytes()
	limited := http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(limited)
	if err != nil {
		// Separating the two matters for the usage row, which is the only
		// record of what happened: "this client sends bodies we refuse" and
		// "this client keeps hanging up mid-upload" are different operational
		// problems with different fixes.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, newError(http.StatusRequestEntityTooLarge, ErrClassRequestTooLarge,
				"request body exceeds the "+strconv.FormatInt(limit, 10)+" byte limit")
		}
		return nil, newError(http.StatusBadRequest, ErrClassInvalidRequest,
			"request body could not be read")
	}
	if len(body) == 0 {
		return nil, newError(http.StatusBadRequest, ErrClassInvalidRequest, "request body is required")
	}
	return body, nil
}

// rewriteModel swaps the catalog id for the provider model name, preserving
// every other field byte-for-byte in value (key order is not meaningful in
// JSON). Working on a RawMessage map rather than a typed struct is what keeps
// the gateway forward-compatible with provider fields we have never heard of.
func rewriteModel(body []byte, upstreamModel string) ([]byte, *GatewayError) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, newError(http.StatusBadRequest, ErrClassInvalidRequest,
			"request body must be a JSON object")
	}
	encoded, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, newError(http.StatusInternalServerError, ErrClassInternal, "failed to prepare request")
	}
	object["model"] = encoded
	out, err := json.Marshal(object)
	if err != nil {
		return nil, newError(http.StatusInternalServerError, ErrClassInternal, "failed to prepare request")
	}
	return out, nil
}

// relay forwards a successful upstream response and meters it.
//
// Streaming is a straight tee: every chunk that arrives is written to the
// client and flushed immediately, and the same chunk passes through a usage
// scanner. Nothing is held back to compute metering — the scanner is a
// side-channel on bytes already in flight, so the Desktop sees frame N before
// the upstream has produced frame N+1.
func (g *Gateway) relay(w http.ResponseWriter, resp *http.Response, protocol Protocol, record *UsageRecord) {
	copyResponseHeaders(w.Header(), resp.Header)
	record.HTTPStatus = resp.StatusCode
	record.Status = model.DesktopModelGatewayUsageStatusCompleted

	if !isStreamingResponse(resp) {
		body, err := readBufferedBody(resp.Body)
		if err != nil {
			// Headers are not written yet, so this can still be a clean error.
			record.Status = model.DesktopModelGatewayUsageStatusFailed
			record.ErrorClass = ErrClassUpstreamError
			record.HTTPStatus = http.StatusBadGateway
			writeError(w, protocol, newError(http.StatusBadGateway, ErrClassUpstreamError,
				"the upstream model provider closed the response early"))
			return
		}
		record.Usage = ParseUsageJSON(body)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	// Disable any intermediary buffering that would defeat per-frame flush.
	if w.Header().Get("X-Accel-Buffering") == "" {
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)

	scanner := NewUsageScanner()
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	sink := &flushWriter{w: w, flusher: flusher}
	if _, err := io.Copy(io.MultiWriter(sink, scanner), resp.Body); err != nil {
		// The status line is long gone; the only honest thing left is to stop
		// writing and record that the stream did not complete. Whatever the
		// upstream billed us for before the break is still metered below.
		record.Status = model.DesktopModelGatewayUsageStatusFailed
		record.ErrorClass = ErrClassUpstreamError
	}
	record.Usage = scanner.Usage()
}

// flushWriter flushes after every chunk. Without it, Go's response buffer
// would batch SSE frames and the Desktop's tool loop would see a turn arrive
// in one lump at the end — which is functionally a non-streaming gateway.
type flushWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

// fail records the refusal on the usage row and answers in the caller's
// protocol.
func (g *Gateway) fail(w http.ResponseWriter, protocol Protocol, record *UsageRecord, err *GatewayError) {
	record.Status = model.DesktopModelGatewayUsageStatusFailed
	record.ErrorClass = err.Class
	record.HTTPStatus = err.Status
	writeError(w, protocol, err)
}

func writeError(w http.ResponseWriter, protocol Protocol, err *GatewayError) {
	if err.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(err.RetryAfterSeconds))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(err.Status)
	_ = json.NewEncoder(w).Encode(err.ErrorBody(protocol))
}

func rateLimitMessage(reason string) string {
	switch reason {
	case "concurrent_limit":
		return "too many concurrent model requests for this account"
	case "global_busy":
		return "the model gateway is at capacity, retry shortly"
	default:
		return "too many model requests, retry shortly"
	}
}

// numericRetryAfter accepts only the delta-seconds form. The HTTP-date form
// is dropped rather than converted: it would echo the upstream's clock, which
// is one more fact about our provider than a client needs.
func numericRetryAfter(value string) int {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 1
	}
	if seconds > 300 {
		return 300
	}
	return seconds
}
