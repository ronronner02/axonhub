package orchestrator

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
)

func rawProviderCaptureMiddlewares(outbound *PersistentOutboundTransformer, systemService *biz.SystemService) []pipeline.Middleware {
	return []pipeline.Middleware{
		captureRawProviderResponse(outbound, systemService),
		captureRawProviderStream(outbound, systemService),
		recoverAnthropicStream(outbound, systemService.CompatibilityConfig),
	}
}

// recoverAnthropicStream 只修复启动配置白名单中的渠道，并在透传分流前运行，
// 确保转换管线与原始透传观察到完全相同的事件序列。
func recoverAnthropicStream(outbound *PersistentOutboundTransformer, config biz.CompatibilityConfig) pipeline.Middleware {
	return pipeline.OnRawStream("recover-anthropic-stream", func(ctx context.Context, stream streams.Stream[*httpclient.StreamEvent]) (streams.Stream[*httpclient.StreamEvent], error) {
		channel := outbound.GetCurrentChannel()
		if channel == nil || !config.AnthropicStreamRecoveryEnabledFor(channel.ID, channel.Name) {
			return stream, nil
		}

		rawRequest := outbound.state.RawProviderRequest
		if rawRequest == nil || llm.APIFormat(rawRequest.APIFormat) != llm.APIFormatAnthropicMessage {
			return stream, nil
		}

		return &anthropicTerminalRecoveryStream{
			ctx:         ctx,
			stream:      stream,
			channelID:   channel.ID,
			channelName: channel.Name,
			openBlocks:  make(map[string]struct{}),
		}, nil
	})
}

// anthropicTerminalRecoveryStream 只修复语义完整的 message_delta 后直接 EOF、
// 未发送 message_stop 的上游缺陷。显式错误、传输失败或未闭合内容块不会被修复。
//
//nolint:containedctx // Next 需要在 EOF 时区分正常结束和请求取消。
type anthropicTerminalRecoveryStream struct {
	ctx         context.Context
	stream      streams.Stream[*httpclient.StreamEvent]
	channelID   int
	channelName string
	current     *httpclient.StreamEvent

	messageStarted   bool
	terminalSeen     bool
	explicitError    bool
	semanticComplete bool
	syntheticSent    bool
	blockStateUnsafe bool
	openBlocks       map[string]struct{}
}

var _ streams.Stream[*httpclient.StreamEvent] = (*anthropicTerminalRecoveryStream)(nil)

func (s *anthropicTerminalRecoveryStream) Next() bool {
	if s.stream.Next() {
		s.current = s.stream.Current()
		s.observe(s.current)

		return true
	}

	if s.syntheticSent || s.terminalSeen || s.explicitError || !s.messageStarted || !s.semanticComplete || s.blockStateUnsafe || len(s.openBlocks) > 0 || s.stream.Err() != nil || s.ctx.Err() != nil {
		return false
	}

	s.syntheticSent = true
	s.terminalSeen = true
	s.current = &httpclient.StreamEvent{
		Type: "message_stop",
		Data: []byte(`{"type":"message_stop"}`),
	}

	log.Warn(s.ctx, "Synthesizing missing Anthropic message_stop after semantic completion",
		log.Int("channel_id", s.channelID),
		log.String("channel", s.channelName),
	)

	return true
}

func (s *anthropicTerminalRecoveryStream) Current() *httpclient.StreamEvent {
	return s.current
}

func (s *anthropicTerminalRecoveryStream) Err() error {
	return s.stream.Err()
}

func (s *anthropicTerminalRecoveryStream) Close() error {
	return s.stream.Close()
}

func (s *anthropicTerminalRecoveryStream) observe(event *httpclient.StreamEvent) {
	if event == nil {
		return
	}

	eventType := strings.TrimSpace(event.Type)
	if eventType == "" {
		eventType = gjson.GetBytes(event.Data, "type").String()
	}

	switch eventType {
	case "message_start":
		s.messageStarted = true
	case "content_block_start":
		if index := anthropicEventIndex(event.Data); index != "" {
			s.openBlocks[index] = struct{}{}
		} else {
			s.blockStateUnsafe = true
		}
	case "content_block_stop":
		if index := anthropicEventIndex(event.Data); index != "" {
			delete(s.openBlocks, index)
		} else {
			s.blockStateUnsafe = true
		}
	case "message_delta":
		stopReason := strings.TrimSpace(gjson.GetBytes(event.Data, "delta.stop_reason").String())
		if stopReason != "" {
			s.semanticComplete = true
		}
	case "message_stop":
		s.terminalSeen = true
	case "error":
		s.explicitError = true
	}
}

func anthropicEventIndex(data []byte) string {
	index := gjson.GetBytes(data, "index")
	if !index.Exists() {
		return ""
	}

	return index.Raw
}
