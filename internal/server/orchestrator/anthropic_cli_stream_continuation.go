package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
)

const claudeStreamContinuationFeedback = "上一条回复只报告了进度或下一步计划。请继续完成用户原任务所需的检查、工具调用或交付；如果工作已经完成，请直接给出实际结果；确实需要用户补充信息时请明确说明。不要扩大原任务范围，不要重复进度句。"

var (
	claudeShortAnswerRequestRE = regexp.MustCompile(`(?i)(只(需|要)?(回复|输出|回答)|一句话|简短(回答|回复)|不要(继续|执行|调用)|只.{0,8}(计划|方案|建议)|reply only|answer only|one sentence|do not (continue|run|use))`)
	claudeActionRequestRE      = regexp.MustCompile(`(?i)(继续|接着|完成|帮我|先看|请.{0,16}(检查|查看|修复|生成|给出)|^(检查|查看|修复|排查|读取|实现|生成|提交)|\b(continue|proceed|finish|implement|fix)\b)`)
	claudeProgressActionRE     = regexp.MustCompile(`(?i)((我|我们)?(现在|接下来|下一步|然后|先|再|继续)[，,:：\s]*(看|查|检查|读取|核对|确认|定位|分析|写|修复|完成|出(单|工单|今天))|看代码(实际)?(写|做|实现)到|查一下再|(?:I'll|I will|Let me|Next I(?:'ll| will))\s+(check|inspect|read|look|verify|implement|continue))`)
	claudeProgressWaitRE       = regexp.MustCompile(`(?i)(请(你)?(先)?(提供|确认|选择|告诉我|补充)|(需要|等待|等)(你|用户)|need (your|you to)|please (provide|confirm|choose))`)
	claudeProgressResultRE     = regexp.MustCompile(`(?i)(已完成|完成了|全部完成|已修复|已解决|已保存|已生成|测试(通过|失败)|^(结论|结果|根因|答案)[：:]|all done|task completed)`)
)

// 复用当前渠道定制后的 executor；一次客户端请求最多追加一次上游调用。
type claudeCLIContinuationExecutor struct {
	pipeline.Executor
	used        *atomic.Bool
	channelID   int
	channelName string
}

func (e *claudeCLIContinuationExecutor) DoStream(ctx context.Context, request *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	if e.used.Load() || !claudeContinuationRequestEligible(request) {
		return e.Executor.DoStream(ctx, request)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	raw, err := e.Executor.DoStream(streamCtx, request)
	if err != nil {
		cancel()
		return nil, err
	}
	if raw == nil {
		cancel()
		return nil, errors.New("Claude 上游未返回流")
	}
	return &claudeCLIContinuationStream{
		ctx:              streamCtx,
		cancel:           cancel,
		executor:         e,
		request:          request,
		stream:           raw,
		safe:             true,
		progressEligible: true,
		open:             make(map[int]string),
		usage:            make(map[string]json.RawMessage),
	}, nil
}

//nolint:containedctx // 续作沿用原请求的取消和截止时间，Close 会取消正在连接的第二段。
type claudeCLIContinuationStream struct {
	ctx              context.Context
	cancel           context.CancelFunc
	executor         *claudeCLIContinuationExecutor
	request          *httpclient.Request
	mu               sync.Mutex
	stream           streams.Stream[*httpclient.StreamEvent]
	closed           bool
	err              error
	current          *httpclient.StreamEvent
	queue            []*httpclient.StreamEvent
	terminal         []*httpclient.StreamEvent
	following        bool
	safe             bool
	started          bool
	stopped          bool
	nextIndex        int
	offset           int
	eof              bool
	progressEligible bool
	finishReason     string
	open             map[int]string
	text             strings.Builder
	usage            map[string]json.RawMessage
	previousUsage    map[string]json.RawMessage
}

func (s *claudeCLIContinuationStream) active() streams.Stream[*httpclient.StreamEvent] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream
}

func (s *claudeCLIContinuationStream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *claudeCLIContinuationStream) Next() bool {
	for {
		if err := s.ctx.Err(); err != nil {
			s.fail(err)
			return false
		}
		if len(s.queue) > 0 {
			s.current, s.queue = s.queue[0], s.queue[1:]
			return true
		}
		if s.eof {
			return false
		}
		raw := s.active()
		if raw == nil {
			return false
		}
		if !raw.Next() {
			s.eof = true
			err := raw.Err()
			if s.ctx.Err() != nil {
				err = s.ctx.Err()
			}
			if err == nil && !s.following && len(s.terminal) > 0 && s.resume() {
				continue
			}
			if s.following {
				if err == nil && (!s.safe || !s.started || s.finishReason == "" || len(s.open) > 0) {
					err = io.ErrUnexpectedEOF
				}
				if err != nil {
					s.terminal = nil
				}
			}
			s.fail(err)
			s.queue, s.terminal = s.terminal, nil
			continue
		}
		event := raw.Current()
		if event == nil {
			s.safe = false
			if s.following {
				s.fail(io.ErrUnexpectedEOF)
				s.eof = true
				return false
			}
			s.current = event
			return true
		}
		s.observe(event)
		kind := claudeStreamEventType(event)
		if s.following {
			if kind == "error" {
				s.fail(errors.New("Claude 续作上游返回错误事件"))
				s.terminal = nil
				s.eof = true
				s.current = event
				return true
			}
			if !s.safe {
				s.fail(errors.New("Claude 续作事件序列不完整"))
				s.eof = true
				return false
			}
			if kind == "message_start" {
				event = &httpclient.StreamEvent{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{}}`)}
			}
			adjusted, err := s.adjust(event)
			if err != nil {
				s.fail(err)
				s.eof = true
				return false
			}
			// 续作的终帧同样等到正常 EOF 再释放，避免把半截回复标记成完成。
			if len(s.terminal) > 0 || kind == "message_stop" || (kind == "message_delta" && s.finishReason != "") {
				if len(s.terminal) >= 8 {
					s.fail(errors.New("Claude 续作终帧过多"))
					s.eof = true
					return false
				}
				s.terminal = append(s.terminal, adjusted)
				continue
			}
			s.current = adjusted
			return true
		}
		if len(s.terminal) > 0 {
			s.terminal = append(s.terminal, event)
			if !s.safe || len(s.terminal) >= 8 {
				s.safe = false
				s.queue, s.terminal = s.terminal, nil
			}
			continue
		}
		if kind == "message_delta" && s.finishReason == "end_turn" && s.safe && s.progressEligible && claudeProgressOnly(s.text.String()) {
			s.terminal = append(s.terminal, event)
			continue
		}
		s.current = event
		return true
	}
}

func (s *claudeCLIContinuationStream) Current() *httpclient.StreamEvent { return s.current }

func (s *claudeCLIContinuationStream) Err() error {
	// fanout 会先 Close 再读 Err，不能把内部清理产生的取消当作传输失败。
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *claudeCLIContinuationStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	raw := s.stream
	s.stream = nil
	s.mu.Unlock()
	s.cancel()
	if raw != nil {
		return raw.Close()
	}
	return nil
}

func claudeStreamEventType(event *httpclient.StreamEvent) string {
	if event.Type != "" {
		return event.Type
	}
	return gjson.GetBytes(event.Data, "type").String()
}

func (s *claudeCLIContinuationStream) observe(event *httpclient.StreamEvent) {
	if !s.following && (!s.safe || !s.progressEligible) {
		return
	}
	if !json.Valid(event.Data) {
		s.safe = false
		return
	}
	kind := claudeStreamEventType(event)
	if s.stopped && kind != "ping" {
		s.safe = false
		return
	}
	switch kind {
	case "message_start":
		if s.started || len(gjson.GetBytes(event.Data, "message.content").Array()) > 0 {
			s.safe = false
		}
		s.started = true
		mergeClaudeUsage(s.usage, gjson.GetBytes(event.Data, "message.usage").Raw)
	case "content_block_start", "content_block_stop", "content_block_delta":
		var header struct {
			Index *int `json:"index"`
		}
		if !s.started || s.finishReason != "" || json.Unmarshal(event.Data, &header) != nil || header.Index == nil || *header.Index < 0 || *header.Index == math.MaxInt {
			s.safe = false
			return
		}
		index := *header.Index
		switch kind {
		case "content_block_start":
			if index != s.nextIndex {
				s.safe = false
				return
			}
			s.nextIndex = index + 1
			blockType := gjson.GetBytes(event.Data, "content_block.type").String()
			if blockType == "" {
				s.safe = false
				return
			}
			s.open[index] = blockType
			if blockType != "text" && blockType != "thinking" && blockType != "redacted_thinking" {
				s.progressEligible = false
			}
			if s.nextIndex > 64 {
				s.progressEligible = false
			}
			if blockType == "text" {
				s.captureText(gjson.GetBytes(event.Data, "content_block.text").String())
			}
		case "content_block_stop":
			if _, exists := s.open[index]; !exists {
				s.safe = false
			}
			delete(s.open, index)
		case "content_block_delta":
			blockType, exists := s.open[index]
			if !exists {
				s.safe = false
				return
			}
			if blockType == "text" {
				if gjson.GetBytes(event.Data, "delta.type").String() != "text_delta" {
					s.progressEligible = false
				}
				s.captureText(gjson.GetBytes(event.Data, "delta.text").String())
			}
		}
	case "message_delta":
		if !s.started {
			s.safe = false
		}
		if reason := gjson.GetBytes(event.Data, "delta.stop_reason").String(); reason != "" {
			if s.finishReason != "" || len(s.open) > 0 {
				s.safe = false
			}
			s.finishReason = reason
		}
		mergeClaudeUsage(s.usage, gjson.GetBytes(event.Data, "usage").Raw)
	case "message_stop":
		if !s.started || s.finishReason == "" || len(s.open) > 0 {
			s.safe = false
		}
		s.stopped = true
	case "ping":
	default:
		s.safe = false
	}
}

func (s *claudeCLIContinuationStream) captureText(text string) {
	if s.following || !s.progressEligible {
		return
	}
	// 限制实际写入量；不能先检查长度再追加任意大的单个 chunk。
	if len(text) > 1024-s.text.Len() {
		s.progressEligible = false
		return
	}
	s.text.WriteString(text)
}

func (s *claudeCLIContinuationStream) resume() bool {
	if s.following || !s.safe || !s.progressEligible || !s.started || s.finishReason != "end_turn" || len(s.open) > 0 || s.ctx.Err() != nil {
		return false
	}
	request := claudeBuildContinuationRequest(s.request, s.text.String(), s.usage)
	if request == nil || !s.executor.used.CompareAndSwap(false, true) {
		return false
	}
	// 只移除提前的结束标记；保留本段 usage 和其它终帧元数据。
	for _, event := range s.terminal {
		if claudeStreamEventType(event) == "message_stop" {
			continue
		}
		copy := *event
		copy.Data, _ = sjson.DeleteBytes(event.Data, "delta.stop_reason")
		copy.Data, _ = sjson.DeleteBytes(copy.Data, "delta.stop_sequence")
		s.queue = append(s.queue, &copy)
	}
	s.terminal = nil
	s.mu.Lock()
	old := s.stream
	s.stream = nil
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return true
	}
	if old != nil {
		_ = old.Close()
	}
	log.Warn(s.ctx, "自动续作提前结束的 Claude CLI 流",
		log.Int("channel_id", s.executor.channelID), log.String("channel", s.executor.channelName))
	raw, err := s.executor.Executor.DoStream(s.ctx, request)
	if err != nil {
		s.fail(err)
		return true
	}
	if raw == nil {
		s.fail(errors.New("Claude 续作上游未返回流"))
		return true
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = raw.Close()
		return true
	}
	s.stream = raw
	s.mu.Unlock()
	s.following = true
	s.offset = s.nextIndex
	s.nextIndex = 0
	s.started = false
	s.stopped = false
	s.eof = false
	s.finishReason = ""
	s.previousUsage = s.usage
	s.usage = make(map[string]json.RawMessage)
	s.open = make(map[int]string)
	return true
}

func (s *claudeCLIContinuationStream) adjust(event *httpclient.StreamEvent) (*httpclient.StreamEvent, error) {
	copy := *event
	var err error
	kind := claudeStreamEventType(event)
	if kind == "content_block_start" || kind == "content_block_delta" || kind == "content_block_stop" {
		var header struct {
			Index *int `json:"index"`
		}
		if json.Unmarshal(event.Data, &header) != nil || header.Index == nil || *header.Index < 0 || *header.Index > math.MaxInt-s.offset {
			return nil, errors.New("Claude 续作内容块索引无效")
		}
		copy.Data, err = sjson.SetBytes(event.Data, "index", *header.Index+s.offset)
		if err != nil {
			return nil, err
		}
	}
	if kind == "message_delta" {
		usage, err := addClaudeUsage(s.previousUsage, s.usage)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(usage)
		if err != nil {
			return nil, err
		}
		copy.Data, err = sjson.SetRawBytes(copy.Data, "usage", encoded)
		if err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

func mergeClaudeUsage(target map[string]json.RawMessage, raw string) {
	var update map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &update) != nil {
		return
	}
	for key, value := range update {
		var previous, next map[string]json.RawMessage
		if json.Unmarshal(target[key], &previous) == nil && previous != nil && json.Unmarshal(value, &next) == nil && next != nil {
			mergeClaudeUsage(previous, string(value))
			target[key], _ = json.Marshal(previous)
		} else {
			target[key] = value
		}
	}
}

// 各段内 usage 是累计快照，段与段之间才相加；未知字段保持原始 JSON。
func addClaudeUsage(previous, current map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	result := maps.Clone(previous)
	if result == nil {
		result = make(map[string]json.RawMessage)
	}
	for key, value := range current {
		old, exists := previous[key]
		result[key] = value
		if !exists {
			continue
		}
		var left, right map[string]json.RawMessage
		if json.Unmarshal(old, &left) == nil && left != nil && json.Unmarshal(value, &right) == nil && right != nil {
			combined, err := addClaudeUsage(left, right)
			if err != nil {
				return nil, err
			}
			result[key], _ = json.Marshal(combined)
			continue
		}
		if !strings.HasSuffix(key, "_tokens") && !strings.HasSuffix(key, "_requests") {
			continue
		}
		var a, b int64
		if json.Unmarshal(old, &a) != nil || json.Unmarshal(value, &b) != nil {
			continue
		}
		if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
			return nil, errors.New("Claude 续作用量溢出")
		}
		result[key], _ = json.Marshal(a + b)
	}
	return result, nil
}

func claudeProgressOnly(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 240 || strings.Contains(text, "```") || strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") {
		return false
	}
	if claudeProgressWaitRE.MatchString(text) || claudeProgressResultRE.MatchString(text) {
		return false
	}
	return claudeProgressActionRE.MatchString(text)
}

func claudeContinuationRequestEligible(request *httpclient.Request) bool {
	if request == nil || request.APIFormat != llm.APIFormatAnthropicMessage.String() {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(request.Body, &body) != nil || gjson.GetBytes(request.Body, "stream").Type != gjson.True {
		return false
	}
	var tools []json.RawMessage
	if json.Unmarshal(body["tools"], &tools) != nil || len(tools) == 0 {
		return false
	}
	if gjson.GetBytes(request.Body, "tool_choice.type").String() == "none" || len(gjson.GetBytes(request.Body, "stop_sequences").Array()) > 0 || gjson.GetBytes(request.Body, "output_config.format").Exists() || gjson.GetBytes(request.Body, "output_format").Exists() {
		return false
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(body["messages"], &messages) != nil || len(messages) == 0 || messages[len(messages)-1].Role != "user" {
		return false
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(messages[i].Content, &text) != nil {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(messages[i].Content, &blocks) != nil {
				return false
			}
			hasToolResult := false
			for _, block := range blocks {
				if block.Type == "tool_result" {
					hasToolResult = true
				}
				if block.Type == "text" && !strings.HasPrefix(strings.TrimSpace(block.Text), "<system-reminder>") {
					text += block.Text + "\n"
				}
			}
			if hasToolResult {
				continue
			}
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if claudeShortAnswerRequestRE.MatchString(text) {
			return false
		}
		return claudeActionRequestRE.MatchString(text)
	}
	return false
}

func claudeBuildContinuationRequest(request *httpclient.Request, text string, usage map[string]json.RawMessage) *httpclient.Request {
	var body map[string]json.RawMessage
	if json.Unmarshal(request.Body, &body) != nil {
		return nil
	}
	var maxTokens, spent int64
	if json.Unmarshal(body["max_tokens"], &maxTokens) != nil || json.Unmarshal(usage["output_tokens"], &spent) != nil || gjson.ParseBytes(usage["output_tokens"]).Type != gjson.Number || maxTokens <= 0 || spent < 0 || spent > maxTokens || maxTokens-spent < 256 {
		return nil
	}
	remaining := maxTokens - spent
	if budget := gjson.GetBytes(request.Body, "thinking.budget_tokens").Int(); budget >= remaining {
		return nil
	}
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil {
		return nil
	}
	// 回填可见文字即可，不复制跨调用的 thinking 签名，也不修改原有历史。
	assistant, _ := json.Marshal(map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}})
	feedback, _ := json.Marshal(map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": claudeStreamContinuationFeedback}}})
	messages = append(messages, assistant, feedback)
	body["messages"], _ = json.Marshal(messages)
	body["max_tokens"], _ = json.Marshal(remaining)
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	copy := *request
	copy.Body = encoded
	copy.JSONBody = nil
	copy.Headers = request.Headers.Clone()
	copy.Headers.Del("Content-Length")
	return &copy
}
