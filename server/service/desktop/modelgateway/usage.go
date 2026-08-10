package modelgateway

import (
	"bytes"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"server/globals"
	"server/model"
)

// TokenUsage is the protocol-neutral shape both wire formats collapse into.
// Fields are additive maxima, never sums of successive frames — see Merge.
type TokenUsage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	TotalTokens         int
}

// Total returns the figure stored in the usage row: the upstream's own total
// when it gave one, otherwise the sum of what it did give.
func (u TokenUsage) Total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// Merge folds one observed frame into the running total by taking the maximum
// of each field rather than adding.
//
// That is the correct rule for both protocols. Anthropic reports input tokens
// once in message_start and then re-reports a GROWING output_tokens in each
// message_delta; adding would multiply the bill by the number of deltas.
// OpenAI reports a single cumulative usage object. Max is right for both and
// is idempotent if a frame is somehow seen twice.
func (u *TokenUsage) Merge(other TokenUsage) {
	if other.InputTokens > u.InputTokens {
		u.InputTokens = other.InputTokens
	}
	if other.OutputTokens > u.OutputTokens {
		u.OutputTokens = other.OutputTokens
	}
	if other.CacheReadTokens > u.CacheReadTokens {
		u.CacheReadTokens = other.CacheReadTokens
	}
	if other.CacheCreationTokens > u.CacheCreationTokens {
		u.CacheCreationTokens = other.CacheCreationTokens
	}
	if other.TotalTokens > u.TotalTokens {
		u.TotalTokens = other.TotalTokens
	}
}

// rawUsage is deliberately permissive: it carries every usage field either
// protocol might send, all optional. One struct means one parse per frame and
// no per-protocol branching in the hot path.
type rawUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	PromptTokens             *int `json:"prompt_tokens"`
	CompletionTokens         *int `json:"completion_tokens"`
	TotalTokens              *int `json:"total_tokens"`
}

func (r *rawUsage) toTokenUsage() TokenUsage {
	if r == nil {
		return TokenUsage{}
	}
	usage := TokenUsage{}
	if r.InputTokens != nil {
		usage.InputTokens = *r.InputTokens
	}
	if r.PromptTokens != nil && *r.PromptTokens > usage.InputTokens {
		usage.InputTokens = *r.PromptTokens
	}
	if r.OutputTokens != nil {
		usage.OutputTokens = *r.OutputTokens
	}
	if r.CompletionTokens != nil && *r.CompletionTokens > usage.OutputTokens {
		usage.OutputTokens = *r.CompletionTokens
	}
	if r.CacheReadInputTokens != nil {
		usage.CacheReadTokens = *r.CacheReadInputTokens
	}
	if r.CacheCreationInputTokens != nil {
		usage.CacheCreationTokens = *r.CacheCreationInputTokens
	}
	if r.TotalTokens != nil {
		usage.TotalTokens = *r.TotalTokens
	}
	return usage
}

// usageFrame covers both places a usage object appears: at the top level
// (Anthropic message_delta and non-stream responses, OpenAI everywhere) and
// nested under `message` (Anthropic message_start).
type usageFrame struct {
	Usage   *rawUsage `json:"usage"`
	Message *struct {
		Usage *rawUsage `json:"usage"`
	} `json:"message"`
}

// ParseUsageJSON pulls token usage out of a complete, non-streamed response
// body. A body we cannot parse yields a zero usage rather than an error: an
// unmetered call is a reporting gap, never a reason to fail a request the
// user already paid for in latency.
func ParseUsageJSON(body []byte) TokenUsage {
	var frame usageFrame
	if err := json.Unmarshal(body, &frame); err != nil {
		return TokenUsage{}
	}
	usage := frame.Usage.toTokenUsage()
	if frame.Message != nil {
		usage.Merge(frame.Message.Usage.toTokenUsage())
	}
	return usage
}

// maxUsageScanLine caps how much of one SSE line we will buffer while looking
// for a usage object. A well-behaved frame is a few KB; anything past this is
// either a pathological payload or an upstream streaming without newlines,
// and neither is worth holding in memory to improve a metering figure.
const maxUsageScanLine = 1 << 20

// UsageScanner tees an SSE stream and extracts token usage without buffering
// the response. It implements io.Writer so it can sit in an io.MultiWriter
// beside the flushing client writer: the bytes reach the Desktop the instant
// they arrive, and metering is a side effect of them passing through.
//
// It is intentionally lossy under pressure: an over-long line is dropped
// rather than accumulated. Losing a usage figure is a reporting gap; holding
// an unbounded buffer per in-flight stream is an outage.
type UsageScanner struct {
	buf      []byte
	overflow bool
	usage    TokenUsage
}

func NewUsageScanner() *UsageScanner { return &UsageScanner{} }

// Write consumes a chunk of the upstream stream. It never returns an error —
// an io.MultiWriter aborts the whole copy on a writer error, and a metering
// hiccup must not be able to sever a live model stream.
func (s *UsageScanner) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			s.appendLine(p)
			break
		}
		s.appendLine(p[:idx])
		s.flushLine()
		p = p[idx+1:]
	}
	return n, nil
}

func (s *UsageScanner) appendLine(chunk []byte) {
	if s.overflow {
		return
	}
	if len(s.buf)+len(chunk) > maxUsageScanLine {
		s.overflow = true
		s.buf = s.buf[:0]
		return
	}
	s.buf = append(s.buf, chunk...)
}

func (s *UsageScanner) flushLine() {
	line := bytes.TrimSpace(s.buf)
	s.buf = s.buf[:0]
	wasOverflow := s.overflow
	s.overflow = false
	if wasOverflow || len(line) == 0 {
		return
	}
	// Only `data:` lines carry JSON; `event:`, `id:` and `:` comments do not.
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var frame usageFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return
	}
	s.usage.Merge(frame.Usage.toTokenUsage())
	if frame.Message != nil {
		s.usage.Merge(frame.Message.Usage.toTokenUsage())
	}
}

// Usage returns what has been observed so far. Safe to call after the copy
// finishes; the trailing partial line (a stream cut mid-frame) is deliberately
// not parsed, because a truncated JSON object has no trustworthy numbers.
func (s *UsageScanner) Usage() TokenUsage { return s.usage }

// UsageRecord is one row's worth of facts, assembled by the gateway and
// handed to the recorder once the exchange is over (successfully or not).
type UsageRecord struct {
	UID               uint
	RequestID         string
	Protocol          Protocol
	ModelID           string
	UpstreamModel     string
	ProviderAccountID uint64
	Stream            bool
	Status            string
	HTTPStatus        int
	ErrorClass        string
	Usage             TokenUsage
	StartedAt         time.Time
	Duration          time.Duration
}

// UsageRecorder persists metering rows. An interface so the gateway can be
// exercised without a database and so a future settlement pass can wrap it.
type UsageRecorder interface {
	Record(record UsageRecord)
}

// DBUsageRecorder writes to w_desktop_model_gateway_usage.
type DBUsageRecorder struct {
	db *gorm.DB
}

func NewDBUsageRecorder(db *gorm.DB) *DBUsageRecorder { return &DBUsageRecorder{db: db} }

// Record writes the row best-effort. A metering failure is logged loudly and
// dropped: the model call already happened and the response already reached
// the user, so failing here would only turn a bookkeeping problem into a
// second, user-visible one. The loud log is what makes the gap findable.
func (r *DBUsageRecorder) Record(record UsageRecord) {
	if r == nil || r.db == nil {
		globals.Warn("[ModelGateway] usage not recorded: database is not configured (request " + record.RequestID + ")")
		return
	}
	row := model.DesktopModelGatewayUsage{
		UID:                 record.UID,
		RequestID:           record.RequestID,
		Protocol:            string(record.Protocol),
		ModelID:             record.ModelID,
		UpstreamModel:       record.UpstreamModel,
		ProviderAccountID:   record.ProviderAccountID,
		Stream:              record.Stream,
		Status:              record.Status,
		HTTPStatus:          record.HTTPStatus,
		ErrorClass:          record.ErrorClass,
		InputTokens:         record.Usage.InputTokens,
		OutputTokens:        record.Usage.OutputTokens,
		CacheReadTokens:     record.Usage.CacheReadTokens,
		CacheCreationTokens: record.Usage.CacheCreationTokens,
		TotalTokens:         record.Usage.Total(),
		DurationMS:          int(record.Duration / time.Millisecond),
		StartedAt:           record.StartedAt,
	}
	if err := r.db.Create(&row).Error; err != nil {
		globals.Error("[ModelGateway] failed to record usage for request " + record.RequestID + ": " + err.Error())
	}
}
