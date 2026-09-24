package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
)

func TestAnthropicTerminalRecoveryStream(t *testing.T) {
	t.Parallel()

	start := anthropicRecoveryEvent("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`)
	blockStart := anthropicRecoveryEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	blockStop := anthropicRecoveryEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
	delta := anthropicRecoveryEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	usageDelta := anthropicRecoveryEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":42}}`)
	stop := anthropicRecoveryEvent("message_stop", `{"type":"message_stop"}`)
	errorEvent := anthropicRecoveryEvent("error", `{"type":"error","error":{"type":"server_error","message":"failed"}}`)

	for _, tt := range []struct {
		name          string
		events        []*httpclient.StreamEvent
		streamErr     error
		cancelContext bool
		wantTypes     []string
		wantSynthetic bool
	}{
		{
			name:          "补齐缺失的 message_stop",
			events:        []*httpclient.StreamEvent{start, blockStart, blockStop, delta},
			wantTypes:     []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"},
			wantSynthetic: true,
		},
		{
			name:          "stop_reason 后的 usage delta 不撤销语义完成",
			events:        []*httpclient.StreamEvent{start, blockStart, blockStop, delta, usageDelta},
			wantTypes:     []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_delta", "message_stop"},
			wantSynthetic: true,
		},
		{
			name:      "已有 message_stop 时保持原样",
			events:    []*httpclient.StreamEvent{start, blockStart, blockStop, delta, stop},
			wantTypes: []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"},
		},
		{
			name:      "没有 stop_reason 时不伪造完成",
			events:    []*httpclient.StreamEvent{start, blockStart, blockStop},
			wantTypes: []string{"message_start", "content_block_start", "content_block_stop"},
		},
		{
			name:      "内容块未闭合时不伪造完成",
			events:    []*httpclient.StreamEvent{start, blockStart, delta},
			wantTypes: []string{"message_start", "content_block_start", "message_delta"},
		},
		{
			name:      "显式错误后不补终止事件",
			events:    []*httpclient.StreamEvent{start, blockStart, blockStop, delta, errorEvent},
			wantTypes: []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "error"},
		},
		{
			name:      "传输错误后不补终止事件",
			events:    []*httpclient.StreamEvent{start, blockStart, blockStop, delta},
			streamErr: errors.New("upstream reset"),
			wantTypes: []string{"message_start", "content_block_start", "content_block_stop", "message_delta"},
		},
		{
			name:          "请求取消后不补终止事件",
			events:        []*httpclient.StreamEvent{start, blockStart, blockStop, delta},
			cancelContext: true,
			wantTypes:     []string{"message_start", "content_block_start", "content_block_stop", "message_delta"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			if tt.cancelContext {
				cancel()
			} else {
				defer cancel()
			}

			source := &anthropicRecoveryTestStream{events: tt.events, err: tt.streamErr}
			stream := &anthropicTerminalRecoveryStream{
				ctx:         ctx,
				stream:      source,
				channelID:   18,
				channelName: "100x",
				openBlocks:  make(map[string]struct{}),
			}

			var gotTypes []string
			var gotEvents []*httpclient.StreamEvent
			for stream.Next() {
				event := stream.Current()
				gotEvents = append(gotEvents, event)
				gotTypes = append(gotTypes, event.Type)
			}

			require.Equal(t, tt.wantTypes, gotTypes)
			require.ErrorIs(t, stream.Err(), tt.streamErr)
			if tt.wantSynthetic {
				require.JSONEq(t, `{"type":"message_stop"}`, string(gotEvents[len(gotEvents)-1].Data))
			}
			require.False(t, stream.Next(), "合成终止事件只能发送一次")
		})
	}
}

func TestRecoverAnthropicStream_ChannelScoped(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		channelID int
		channel   string
		apiFormat llm.APIFormat
		selectors []string
		wantWrap  bool
	}{
		{name: "目标渠道按名称启用", channelID: 17, channel: "linxi", apiFormat: llm.APIFormatAnthropicMessage, selectors: []string{"linxi", "100x"}, wantWrap: true},
		{name: "目标渠道按 ID 启用", channelID: 18, channel: "renamed", apiFormat: llm.APIFormatAnthropicMessage, selectors: []string{"18"}, wantWrap: true},
		{name: "白名单为空时默认关闭", channelID: 17, channel: "linxi", apiFormat: llm.APIFormatAnthropicMessage},
		{name: "其他渠道不启用", channelID: 19, channel: "other", apiFormat: llm.APIFormatAnthropicMessage, selectors: []string{"linxi", "100x"}},
		{name: "其他协议不启用", channelID: 17, channel: "linxi", apiFormat: llm.APIFormatOpenAIChatCompletion, selectors: []string{"linxi"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := &PersistenceState{
				CurrentCandidate:   &ChannelModelsCandidate{Channel: &biz.Channel{Channel: &ent.Channel{ID: tt.channelID, Name: tt.channel}}},
				RawProviderRequest: &httpclient.Request{APIFormat: string(tt.apiFormat)},
			}
			outbound := &PersistentOutboundTransformer{state: state}
			middleware := recoverAnthropicStream(outbound, biz.CompatibilityConfig{AnthropicStreamRecoveryChannels: tt.selectors})
			source := streams.SliceStream([]*httpclient.StreamEvent{})

			got, err := middleware.OnOutboundRawStream(context.Background(), source)
			require.NoError(t, err)
			_, wrapped := got.(*anthropicTerminalRecoveryStream)
			require.Equal(t, tt.wantWrap, wrapped)
		})
	}
}

func TestAnthropicRecoveryFeedsPipelineAndPassThrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state := &PersistenceState{
		CurrentCandidate: &ChannelModelsCandidate{Channel: &biz.Channel{Channel: &ent.Channel{
			ID:   18,
			Name: "100x",
			Settings: &objects.ChannelSettings{
				PassThroughBody: lo.ToPtr(true),
			},
		}}},
		OriginalRequestStream: lo.ToPtr(true),
		LlmRequest: &llm.Request{
			APIFormat: llm.APIFormatAnthropicMessage,
			Stream:    lo.ToPtr(true),
		},
		RawProviderRequest: &httpclient.Request{APIFormat: string(llm.APIFormatAnthropicMessage)},
	}
	outbound := &PersistentOutboundTransformer{state: state}
	service := &biz.SystemService{CompatibilityConfig: biz.CompatibilityConfig{
		AnthropicStreamRecoveryChannels: []string{"100x"},
	}}
	events := []*httpclient.StreamEvent{
		anthropicRecoveryEvent("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`),
		anthropicRecoveryEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		anthropicRecoveryEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		anthropicRecoveryEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
	}

	var stream streams.Stream[*httpclient.StreamEvent] = &anthropicRecoveryTestStream{events: events}
	middlewares := rawProviderCaptureMiddlewares(outbound, service)
	for i := len(middlewares) - 1; i >= 0; i-- {
		var err error
		stream, err = middlewares[i].OnOutboundRawStream(ctx, stream)
		require.NoError(t, err)
	}

	pipelineTypes := collectAnthropicRecoveryTypes(stream)
	require.Equal(t, []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"}, pipelineTypes)

	var passThroughTypes []string
	for event := range state.RawStreamCh {
		passThroughTypes = append(passThroughTypes, event.Type)
	}
	require.Equal(t, pipelineTypes, passThroughTypes)
}

func collectAnthropicRecoveryTypes(stream streams.Stream[*httpclient.StreamEvent]) []string {
	var eventTypes []string
	for stream.Next() {
		eventTypes = append(eventTypes, stream.Current().Type)
	}

	return eventTypes
}

func anthropicRecoveryEvent(eventType, data string) *httpclient.StreamEvent {
	return &httpclient.StreamEvent{Type: eventType, Data: []byte(data)}
}

type anthropicRecoveryTestStream struct {
	events []*httpclient.StreamEvent
	index  int
	err    error
}

func (s *anthropicRecoveryTestStream) Next() bool {
	return s.index < len(s.events)
}

func (s *anthropicRecoveryTestStream) Current() *httpclient.StreamEvent {
	event := s.events[s.index]
	s.index++

	return event
}

func (s *anthropicRecoveryTestStream) Err() error {
	return s.err
}

func (s *anthropicRecoveryTestStream) Close() error {
	return nil
}
