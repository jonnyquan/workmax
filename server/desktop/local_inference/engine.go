//go:build desktop

package local_inference

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// 协议常量。MUST 与 desktop.LocalProtocolOpenAICompatible /
// desktop.LocalProtocolAnthropicCompatible 值一致；测试用字面量兜底以防漂移。
const (
	protocolOpenAI    = "openai_compatible"
	protocolAnthropic = "anthropic_compatible"

	// localMaxSSEFrameBytes 对齐 cloud_proxy.maxSSEFrameBytes（1 MiB）。
	localMaxSSEFrameBytes = 1 << 20
)

// doneEventData 是本地推理成功终止时发给 renderer 的 workmax done 事件 Data。
// 已验证 cloud_proxy.classifyDoneForCache 不会据此标 partial（无 is_error /
// code=THREAD_BUSY；logicalSSEEventType 把 Type=="done" 直接识别为 done）。
const doneEventData = `{"type":"done","result":"OK"}`

// ProfileReader 提供当前本地模型配置。依赖倒置：本包不 import desktop
// （否则循环依赖），由 desktop 包提供适配实现。
type ProfileReader interface {
	// 返回 protocol、baseURL、modelID、apiKey。apiKey 可空（如 Ollama 无 key）。
	// baseURL 或 modelID 为空表示本地推理未配置。
	LocalInferenceProfile() (protocol, baseURL, modelID, apiKey string, err error)
}

// KnowledgeHooks bundles the L3c RAG capabilities the local Engine consumes:
// indexing a completed turn for cross-thread long-term memory (L3c-4), and
// retrieving relevant context to inject at the start of a turn (L3c-5).
// Defined here as an interface so this package does not depend on the cgo
// knowledge package; the adapter that satisfies it lives at the wiring site.
// Nil on the Engine → no RAG (turns are not indexed, no context is injected).
type KnowledgeHooks interface {
	IndexTurn(ctx context.Context, turnUUID, userText, assistantText string) error
	Retrieve(ctx context.Context, uid uint64, query string, topK int) ([]RetrievedSource, error)
}

// RetrievedSource is one piece of stored knowledge selected for a turn.
//
// Retrieve used to return bare strings, which was enough to inject but not
// enough to tell anyone what had happened: the model silently answered from
// documents the user could not see, could not check, and could not tell apart
// from the model making something up. Carrying provenance is what turns that
// into a claim the user can audit.
type RetrievedSource struct {
	// Kind is "file" for an uploaded attachment and "conversation" for an
	// earlier indexed turn.
	Kind string
	// Label names it the way the user would: the file name they uploaded, or
	// a description of the conversation it came from.
	Label string
	// Text is the chunk verbatim — what is handed to the model, and what the
	// snippet shown to the user is cut from. Not a summary: a summary would be
	// a second thing that could be wrong.
	Text string
	// Score is 0..1, higher = closer. Derived from the store's L2 distance
	// over normalized embeddings, so it is cosine similarity.
	Score float64
}

// protocolAdapter 封装 OpenAI / Anthropic 兼容 endpoint 的线路差异：
// endpoint 路径、请求体、鉴权头、以及如何从一帧 SSE 提取文本/识别终端。
type protocolAdapter interface {
	endpoint(baseURL string) string
	requestBody(modelID, userText string, atts []Attachment) (io.Reader, error)
	setAuth(req *http.Request, apiKey string)
	// extractText 解析一帧 SSE（已组装好的 event + data），返回文本片段与
	// 是否终端帧。文本为空且 terminal=false 表示该帧为元数据，可跳过。
	extractText(frameEvent, frameData string) (text string, terminal bool)
}

// Engine 是 L1 本地模型 turn runner。它调用用户配置的 OpenAI/Anthropic
// 兼容 endpoint，把上游 SSE 归一化成 workmax SSEEvent 写入 dst，同时用与
// 云端路径相同的 CacheWriter 把文本增量持久化进 w_workagent_message。
//
// Engine 满足 desktop.TurnRunner（duck-typed），与 *cloudproxy.Proxy 在
// streamLegacyAgentTurn 的路由分发处二选一：handleAgentChat 零改动，turn
// intent 幂等 / CacheWriter / renderer SSE / 恢复全部免费复用。
type Engine struct {
	profile    ProfileReader
	db         *gorm.DB
	httpClient *http.Client     // Timeout: 0（SSE turn 可持续数分钟）
	loader     AttachmentLoader // 可 nil（无附件场景）
	hooks      KnowledgeHooks   // 可 nil（L3c-4/5 RAG：索引 turn + 检索注入；nil=关闭）
}

// NewEngine 构造本地推理引擎。db 用于 CacheWriter 持久化本地 turn；loader
// 把 file_ids 解析为模型 context（可 nil，表示该 Engine 不处理附件）；
// hooks 提供 RAG 能力（L3c-4 索引完成的 turn + L3c-5 检索注入），可 nil。
func NewEngine(profile ProfileReader, db *gorm.DB, loader AttachmentLoader, hooks KnowledgeHooks) *Engine {
	return &Engine{
		profile: profile,
		db:      db,
		loader:  loader,
		hooks:   hooks,
		httpClient: &http.Client{
			// 无整体超时——SSE 响应持续整个 turn。空闲由 SSE keepalive 注释
			// 保活，而非 HTTP 超时（对齐 cloud_proxy.Proxy.HTTPClient）。
			Timeout: 0,
		},
	}
}

// Chat 执行一次本地模型 turn。返回 nil = 干净完成（已发 done 事件）；
// 返回 non-nil = 失败（已发 proxy_error 事件，除非是 ctx 取消——取消是用户
// 主动行为，不报错）。满足 desktop.TurnRunner。
func (e *Engine) Chat(ctx context.Context, req cloudproxy.ChatRequest, dst cloudproxy.SSEWriter) (err error) {
	// 1. 读取本地模型配置 + 选择协议适配器。这些失败时 CacheWriter 尚未建立，
	//    直接 emit proxy_error 并返回——不触碰 w_workagent_message。
	protocol, baseURL, modelID, apiKey, perr := e.profile.LocalInferenceProfile()
	if perr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法读取本地模型配置",
			Retryable: false,
			Details:   map[string]any{"reason": perr.Error()},
		})
	}
	if baseURL == "" || modelID == "" {
		// preferred_route=local 但未配置——按 OSS-4 不静默回退云端，显式报错
		// 让用户去设置页填写 base_url / model_id。
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地模型未配置，请在设置中填写 base_url 与 model_id",
			Retryable: false,
		})
	}
	adapter, aerr := selectAdapter(protocol)
	if aerr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindBadRequest,
			Message:   aerr.Error(),
			Retryable: false,
		})
	}

	// 2. 幂等 key + CacheWriter。复用与云端路径相同的 key（desktop-turn:<uuid>），
	//    使 cloud↔local 切换 / replay 命中同一行 w_workagent_message。
	requestID, ridErr := cloudproxy.DesktopTurnRequestID(req.TurnUUID)
	if ridErr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:    cloudproxy.KindBadRequest,
			Message: "invalid turn_uuid",
		})
	}
	cache, cerr := cloudproxy.NewCacheWriter(e.db, cloudproxy.CacheWriterParams{
		UID:                   req.UID,
		ThreadID:              req.ThreadID,
		ThreadUUID:            req.ThreadUUID,
		MessageIdempotencyKey: requestID,
		UserText:              req.UserText,
		ChatMode:              req.ChatMode,
	})
	if cerr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地推理缓存初始化失败",
			Retryable: false,
			Details:   map[string]any{"reason": cerr.Error()},
		})
	}
	// 命名返回值 err 在 defer 闭包里被 Finalize 捕获：nil→complete，非 nil→partial。
	defer func() { _ = cache.Finalize(err) }()

	// 3. 加载附件（如有）→ 模型 context（图片 base64 / 文档提取文本）。
	var atts []Attachment
	if e.loader != nil && len(req.FileIDs) > 0 {
		loaded, lerr := e.loader.Load(req.FileIDs, req.UID)
		if lerr != nil {
			return emitProxyError(dst, cloudproxy.ProxyError{
				Kind:    cloudproxy.KindBadRequest,
				Message: "附件加载失败",
				Details: map[string]any{"reason": lerr.Error()},
			})
		}
		atts = loaded
	}

	// 4. 构造请求。L3c-5：若启用 RAG，先检索相关 chunk 注入到 user text 前。
	userText := req.UserText
	if e.hooks != nil {
		if found, rerr := e.hooks.Retrieve(ctx, req.UID, req.UserText, retrievalTopK); rerr != nil {
			// 检索失败不阻断 turn，仅记日志、不带 context 继续。
			log.Printf("knowledge: retrieve for turn %s: %v", req.TurnUUID, rerr)
		} else if len(found) > 0 {
			var used []RetrievedSource
			userText, used = prependKnowledgeContext(req.UserText, found)
			// Announced before the first token, and built from `used` rather
			// than `found`: the char budget below drops the tail, and a panel
			// listing sources the model never saw would be a confident lie.
			emitRetrievalEvent(dst, used)
		}
	}
	body, berr := adapter.requestBody(modelID, userText, atts)
	if berr != nil {
		_ = dst.WriteProxyError(cloudproxy.ProxyError{Kind: cloudproxy.KindBadRequest, Message: "本地推理请求构造失败"})
		return berr
	}
	httpReq, herr := http.NewRequestWithContext(ctx, http.MethodPost, adapter.endpoint(baseURL), body)
	if herr != nil {
		_ = dst.WriteProxyError(cloudproxy.ProxyError{Kind: cloudproxy.KindBadRequest, Message: "本地推理请求构造失败"})
		return herr
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	adapter.setAuth(httpReq, apiKey)

	// 5. 发起请求。
	resp, doErr := e.httpClient.Do(httpReq)
	if doErr != nil {
		// 用户取消：不 emit proxy_error（renderer 隐藏取消气泡），但仍返回 non-nil
		// 让外层 finalize 把 turn 标 interrupted、cache 标 partial。
		if errors.Is(doErr, context.Canceled) || errors.Is(doErr, context.DeadlineExceeded) {
			return doErr
		}
		pe := cloudproxy.ClassifyNetworkError(doErr)
		_ = dst.WriteProxyError(pe)
		return doErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		pe := cloudproxy.ClassifyHTTPResponse(resp, raw)
		_ = dst.WriteProxyError(pe)
		return fmt.Errorf("local inference: upstream status %d", resp.StatusCode)
	}

	// 6. 归一化上游 SSE → workmax SSEEvent，写 dst + cache。同时累积 assistant
	//    文本，turn 成功后异步索引进知识库（L3c-4 跨线程长期记忆）。
	var assistantText strings.Builder
	err = pipeLocalSSE(resp.Body, adapter, dst, cache, &assistantText)
	if err == nil && e.hooks != nil {
		go e.indexCompletedTurn(req.TurnUUID, req.UserText, assistantText.String())
	}
	return
}

// indexCompletedTurn runs L3c-4 conversation indexing asynchronously so the
// turn response is not blocked by embedding. Failures are logged only: a turn
// that fails to index simply is not retrievable via RAG memory until re-indexed.
func (e *Engine) indexCompletedTurn(turnUUID, userText, assistantText string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := e.hooks.IndexTurn(ctx, turnUUID, userText, assistantText); err != nil {
		log.Printf("knowledge: index turn %s: %v", turnUUID, err)
	}
}

// retrievalTopK / maxRetrievalContextChars cap how much knowledge context is
// injected per turn (plan L3c-5: "控制注入量 top-k + token 上限").
const (
	retrievalTopK            = 4
	maxRetrievalContextChars = 1500
	knowledgeContextPreamble = "以下是知识库中可能相关的内容，回答时可参考：\n"
)

// prependKnowledgeContext packs retrieved sources (best first) under a char
// budget and prepends them to the user text as model context. It returns the
// sources that actually made it in, so the caller can report exactly what the
// model was given rather than what the search returned.
func prependKnowledgeContext(userText string, sources []RetrievedSource) (string, []RetrievedSource) {
	var b strings.Builder
	b.WriteString(knowledgeContextPreamble)
	used := 0
	injected := make([]RetrievedSource, 0, len(sources))
	for _, s := range sources {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if used+len(text) > maxRetrievalContextChars {
			break
		}
		b.WriteString("- ")
		b.WriteString(text)
		b.WriteByte('\n')
		used += len(text) + 2
		s.Text = text
		injected = append(injected, s)
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(userText))
	if len(injected) == 0 {
		// Every candidate was blank or over budget: no context was added, so
		// hand back the untouched prompt rather than a preamble introducing
		// an empty list.
		return userText, nil
	}
	return b.String(), injected
}

// retrievalSnippetChars bounds what travels to the renderer per source. The
// full chunk is what the model reads; the user needs enough to recognise the
// passage, not the whole document echoed down an SSE frame.
const retrievalSnippetChars = 240

// emitRetrievalEvent tells the renderer what grounded the answer, before the
// answer starts arriving.
//
// It is a non-terminal event on the same stream the text arrives on, so it
// cannot race the turn it describes. A renderer that does not know the event
// name surfaces it as "unknown" and carries on — the shim's unknown-event path
// exists for exactly this kind of addition.
func emitRetrievalEvent(dst cloudproxy.SSEWriter, sources []RetrievedSource) {
	if len(sources) == 0 {
		return
	}
	type wireSource struct {
		Kind    string  `json:"kind"`
		Label   string  `json:"label"`
		Snippet string  `json:"snippet"`
		Score   float64 `json:"score"`
	}
	wire := make([]wireSource, 0, len(sources))
	for _, s := range sources {
		wire = append(wire, wireSource{
			Kind:    s.Kind,
			Label:   s.Label,
			Snippet: truncateRunes(s.Text, retrievalSnippetChars),
			Score:   s.Score,
		})
	}
	payload, err := json.Marshal(map[string]any{"sources": wire})
	if err != nil {
		log.Printf("knowledge: marshal retrieval event: %v", err)
		return
	}
	if werr := dst.WriteEvent(cloudproxy.SSEEvent{Type: "retrieval", Data: string(payload)}); werr != nil {
		// The renderer is gone or the socket is dead. The turn's own writes
		// will fail the same way and report it; this one is not worth failing
		// a turn that can still be persisted to the local cache.
		log.Printf("knowledge: write retrieval event: %v", werr)
	}
}

// truncateRunes cuts on a rune boundary. Cutting on bytes would split a
// multi-byte character and produce invalid UTF-8, which the renderer's SSE
// decoder is (correctly) fatal about.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// pipeLocalSSE 读取上游 SSE 流，按 adapter 解析每帧，归一化成 workmax
// text_delta / done 事件写入 dst，并把文本片段 Enqueue 进 cache。
// 收到终端帧（OpenAI [DONE] / Anthropic message_stop）→ 干净返回 nil。
// EOF 前未见终端帧 → emit proxy_error 并返回 non-nil（截断，cache 自动 partial）。
func pipeLocalSSE(src io.Reader, adapter protocolAdapter, dst cloudproxy.SSEWriter, cache *cloudproxy.CacheWriter, assistantText *strings.Builder) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), localMaxSSEFrameBytes)

	var (
		curEvent  string
		curData   strings.Builder
		frameSize int
	)

	// flushFrame 解析并发出当前累积帧，返回是否终端。terminal=true 时已发 done。
	flushFrame := func() (terminal bool, err error) {
		defer func() {
			curEvent = ""
			curData.Reset()
			frameSize = 0
		}()
		text, term := adapter.extractText(curEvent, curData.String())
		if term {
			if werr := dst.WriteEvent(cloudproxy.SSEEvent{Type: "done", Data: doneEventData}); werr != nil {
				return true, werr
			}
			_ = cache.Enqueue("done", "")
			return true, nil
		}
		if text != "" {
			data, _ := json.Marshal(map[string]string{"delta": text})
			if werr := dst.WriteEvent(cloudproxy.SSEEvent{Type: "text_delta", Data: string(data)}); werr != nil {
				return false, werr
			}
			_ = cache.Enqueue("text_delta", text)
			if assistantText != nil {
				assistantText.WriteString(text)
			}
		}
		return false, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// 空行 = 帧边界。
			if frameSize > 0 {
				term, ferr := flushFrame()
				if ferr != nil {
					return ferr
				}
				if term {
					return nil // 干净 done
				}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE 注释（上游 keepalive）
		}
		frameSize += len(line)
		if frameSize > localMaxSSEFrameBytes {
			_ = dst.WriteProxyError(cloudproxy.ProxyError{
				Kind:      cloudproxy.KindServiceUnavailable,
				Message:   "本地模型单帧响应过大",
				Retryable: false,
			})
			return errors.New("local inference: SSE frame exceeds size limit")
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			d = strings.TrimPrefix(d, " ") // SSE 允许 data: 后零或一空格
			if curData.Len() > 0 {
				curData.WriteString("\n")
			}
			curData.WriteString(d)
		}
	}
	if serr := scanner.Err(); serr != nil {
		if errors.Is(serr, context.Canceled) || errors.Is(serr, context.DeadlineExceeded) {
			return serr
		}
		pe := cloudproxy.ClassifyUpstreamStreamError(serr)
		_ = dst.WriteProxyError(pe)
		return serr
	}
	// flush EOF 前最后一帧（部分上游无尾随空行）。
	if frameSize > 0 {
		if _, ferr := flushFrame(); ferr != nil {
			return ferr
		}
	}
	// 走到 EOF 仍未见终端帧 = 截断（done 会在 flushFrame 内 return nil 提前退出）。
	_ = dst.WriteProxyError(cloudproxy.ProxyError{
		Kind:      cloudproxy.KindServiceUnavailable,
		Message:   "本地模型连接中断，未收到完整响应",
		Retryable: true,
	})
	return errors.New("local inference: stream truncated before terminal event")
}

// selectAdapter 按协议常量选择线路适配器。
func selectAdapter(protocol string) (protocolAdapter, error) {
	switch protocol {
	case protocolOpenAI:
		return openaiAdapter{}, nil
	case protocolAnthropic:
		return anthropicAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported local protocol %q", protocol)
	}
}

// emitProxyError 写一个 proxy_error 事件给 renderer，并返回非 nil error
// （让外层 streamLegacyAgentTurn 的 finalize 把 turn 标 interrupted）。
func emitProxyError(dst cloudproxy.SSEWriter, pe cloudproxy.ProxyError) error {
	_ = dst.WriteProxyError(pe)
	return fmt.Errorf("local inference: %s", pe.Kind)
}
