package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	agentv1 "server/contracts/agent/v1"
	"server/middleware"
	workagentmodel "server/model/workagent"
	workagentservice "server/service/tools/workagent"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxPutThreadBodyBytes = 4 << 10

// ThreadApi owns Desktop OAuth resource mutations for Agent threads. It is
// deliberately separate from the legacy /api/work-agent browser-era surface:
// Desktop access tokens receive RFC 6750 HTTP status semantics and cannot fall
// back to cookies or generic portal JWT behavior.
type ThreadApi struct {
	lifecycle *workagentservice.ThreadLifecycleService
}

func NewThreadApi(db *gorm.DB) *ThreadApi {
	if db == nil {
		return &ThreadApi{}
	}
	return &ThreadApi{lifecycle: workagentservice.NewThreadLifecycleService(db)}
}

type putThreadRequest struct {
	Name      string `json:"name"`
	AgentMode string `json:"agent_mode"`
}

type threadResource struct {
	CloudThreadID string `json:"cloud_thread_id"`
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	AgentMode     string `json:"agent_mode"`
	AgentType     string `json:"agent_type"`
	Model         string `json:"model"`
	MessageCount  int    `json:"message_count"`
	MsgPreview    string `json:"msg_preview"`
	FileCount     int    `json:"file_count"`
	IsPublic      bool   `json:"is_public"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type putThreadResponse struct {
	Thread  threadResource `json:"thread"`
	Created bool           `json:"created"`
}

// PutThread handles PUT /api/desktop/agent/threads/:uuid.
//
// The caller supplies a cryptographically random v4 UUID and keeps it stable
// for retries. The server creates the resource once and returns the same owned
// row on replay, making a 401 refresh, response loss, or failed local-cache
// commit safe to retry without creating duplicate conversations.
func (a *ThreadApi) PutThread(c *gin.Context) {
	if a == nil || a.lifecycle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	claims, ok := middleware.OAuthClaims(c)
	if !ok || claims.BaseClaims.Id == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	threadUUID, err := canonicalV4UUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	var request putThreadRequest
	if err := decodeStrictJSON(c, maxPutThreadBodyBytes, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	request.Name, err = normalizeThreadName(request.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
		return
	}
	request.AgentMode = strings.TrimSpace(request.AgentMode)
	if _, allowed := agentv1.OfficialAgentModeSet()[request.AgentMode]; !allowed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_agent_mode"})
		return
	}

	thread, created, err := a.lifecycle.PutThreadNew(
		int(claims.BaseClaims.Id),
		threadUUID,
		request.Name,
		request.AgentMode,
		0,
	)
	if err != nil {
		if errors.Is(err, workagentservice.ErrThreadUUIDOwnedByAnotherUser) {
			c.JSON(http.StatusConflict, gin.H{"error": "thread_uuid_conflict"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_unavailable"})
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, putThreadResponse{
		Thread:  resourceFromThread(thread),
		Created: created,
	})
}

func resourceFromThread(thread *workagentmodel.ChatThread) threadResource {
	return threadResource{
		CloudThreadID: strconv.FormatUint(uint64(thread.Id), 10),
		UUID:          thread.UUID,
		Name:          thread.Name,
		AgentMode:     thread.AgentMode,
		AgentType:     thread.AgentType,
		Model:         thread.Model,
		MessageCount:  thread.MessageCount,
		MsgPreview:    thread.MsgPreview,
		FileCount:     thread.FileCount,
		IsPublic:      thread.IsPublic,
		CreatedAt:     thread.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt:     thread.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
}

func canonicalV4UUID(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("uuid is required")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed.String() != value {
		return "", errors.New("uuid must be canonical v4")
	}
	return value, nil
}

func normalizeThreadName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || !utf8.ValidString(name) || len(name) > 200 {
		return "", errors.New("invalid thread name")
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("invalid thread name")
		}
	}
	return name, nil
}

// decodeStrictJSON keeps the Desktop mutation surface exact: one JSON object,
// one canonical Content-Type, no unknown fields, and no duplicate top-level
// keys whose last-value-wins behavior could split validation from execution.
func decodeStrictJSON(c *gin.Context, maxBytes int64, target any) error {
	contentTypes := c.Request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return errors.New("Content-Type must appear exactly once")
	}
	mediaType, params, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" ||
		(len(params) != 0 && (len(params) != 1 || !strings.EqualFold(params["charset"], "utf-8"))) {
		return errors.New("Content-Type must be application/json")
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes))
	if err != nil {
		return err
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8 JSON")
	}
	allowedFields, err := exactJSONFieldNames(target)
	if err != nil {
		return err
	}
	if err := rejectDuplicateTopLevelKeys(body, allowedFields); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func exactJSONFieldNames(target any) (map[string]struct{}, error) {
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || targetType.Elem().Kind() != reflect.Struct {
		return nil, errors.New("JSON target must be a pointer to a struct")
	}
	targetType = targetType.Elem()
	allowed := make(map[string]struct{}, targetType.NumField())
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		allowed[name] = struct{}{}
	}
	return allowed, nil
}

func rejectDuplicateTopLevelKeys(body []byte, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("request must contain one JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("request object key is malformed")
		}
		if _, exact := allowed[key]; !exact {
			return fmt.Errorf("unknown or non-canonical JSON key %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate JSON key %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("request must contain one JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}
