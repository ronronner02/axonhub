package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
)

type cliContinuationTestExecutor struct {
	requests  []*httpclient.Request
	responses [][]*httpclient.StreamEvent
	onStream  func(context.Context, *httpclient.Request, int) (streams.Stream[*httpclient.StreamEvent], error)
}

func (e *cliContinuationTestExecutor) Do(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	panic("本测试只允许流式请求")
}

func (e *cliContinuationTestExecutor) DoStream(ctx context.Context, request *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	e.requests = append(e.requests, request)
	index := len(e.requests) - 1
	if e.onStream != nil {
		return e.onStream(ctx, request, index)
	}
	if index >= len(e.responses) {
		index = len(e.responses) - 1
	}
	return streams.SliceStream(e.responses[index]), nil
}

func cliContinuationTextEvents(id, text string) []*httpclient.StreamEvent {
	delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
	return []*httpclient.StreamEvent{
		{Type: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"` + id + `","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":20}}}`)},
		{Type: "content_block_start", Data: []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)},
		{Type: "content_block_delta", Data: delta},
		{Type: "content_block_stop", Data: []byte(`{"type":"content_block_stop","index":0}`)},
		{Type: "message_delta", Data: []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`)},
		{Type: "message_stop", Data: []byte(`{"type":"message_stop"}`)},
	}
}

func cliContinuationOutbound(t *testing.T, channel, userAgent, body string, selectors []string) (*PersistentOutboundTransformer, *httpclient.Request) {
	t.Helper()
	state := &PersistenceState{
		CurrentCandidate: &ChannelModelsCandidate{Channel: &biz.Channel{Channel: &ent.Channel{ID: 18, Name: channel}}},
		LlmRequest:       &llm.Request{RawRequest: &httpclient.Request{Headers: http.Header{"User-Agent": []string{userAgent}}}},
	}
	outbound := &PersistentOutboundTransformer{state: state}
	original := &httpclient.Request{APIFormat: llm.APIFormatAnthropicMessage.String(), Body: []byte(body), Headers: http.Header{"Content-Length": []string{"1"}}}
	request, err := guardClaudeCLIContinuation(outbound, biz.CompatibilityConfig{AnthropicStreamRecoveryChannels: selectors}).OnOutboundRawRequest(context.Background(), original)
	require.NoError(t, err)
	return outbound, request
}

const cliContinuationRequest = `{"model":"claude-opus-5","stream":true,"max_tokens":1024,"system":[{"type":"text","text":"keep","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"继续完成"}]}`

func TestClaudeCLIStreamContinuation_RealEarlyEndSamples(t *testing.T) {
	for _, text := range []string{
		"先看 `E:\\demo` 的实际落点，再出今天工单。",
		"`E:\\demo` 找到了，有 git、有 remote。看代码实际写到哪一步。",
		"仓库可见性是安全前提，查一下再出单。",
	} {
		t.Run(text, func(t *testing.T) {
			outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1.282", cliContinuationRequest, []string{"100x", "linxi"})
			executor := &cliContinuationTestExecutor{responses: [][]*httpclient.StreamEvent{cliContinuationTextEvents("msg_first", text), cliContinuationTextEvents("msg_final", "任务已完成，工单如下。")}}
			originalBody := append([]byte(nil), request.Body...)
			stream, err := outbound.CustomizeExecutor(executor).DoStream(context.Background(), request)
			require.NoError(t, err)
			defer stream.Close()
			chunks, err := streams.All(stream)
			require.NoError(t, err)
			require.Len(t, executor.requests, 2, "AxonHub 应在同一客户端请求内自动续作，不依赖本地 hook")
			require.Equal(t, originalBody, request.Body, "原请求保持不变")
			require.Nil(t, executor.requests[1].JSONBody)
			require.Empty(t, executor.requests[1].Headers.Get("Content-Length"))
			require.Equal(t, int64(1004), gjson.GetBytes(executor.requests[1].Body, "max_tokens").Int())
			require.Equal(t, "assistant", gjson.GetBytes(executor.requests[1].Body, "messages.1.role").String())
			require.Equal(t, text, gjson.GetBytes(executor.requests[1].Body, "messages.1.content.0.text").String())
			require.Equal(t, "user", gjson.GetBytes(executor.requests[1].Body, "messages.2.role").String())
			starts, stops, terminals := 0, 0, 0
			for _, chunk := range chunks {
				switch chunk.Type {
				case "message_start":
					starts++
				case "message_stop":
					stops++
				case "message_delta":
					if gjson.GetBytes(chunk.Data, "delta.stop_reason").String() != "" {
						terminals++
					}
				}
			}
			require.Equal(t, 1, starts)
			require.Equal(t, 1, stops)
			require.Equal(t, 1, terminals)
			aggregate, meta, err := anthropic.AggregateStreamChunks(context.Background(), chunks, anthropic.PlatformDirect)
			require.NoError(t, err)
			require.Equal(t, "msg_first", gjson.GetBytes(aggregate, "id").String())
			require.Equal(t, "任务已完成，工单如下。", gjson.GetBytes(aggregate, "content.1.text").String())
			require.Equal(t, int64(200), gjson.GetBytes(aggregate, "usage.input_tokens").Int())
			require.Equal(t, int64(40), gjson.GetBytes(aggregate, "usage.output_tokens").Int())
			require.Equal(t, int64(40), gjson.GetBytes(aggregate, "usage.cache_read_input_tokens").Int())
			require.Equal(t, "end_turn", gjson.GetBytes(aggregate, "stop_reason").String())
			require.Equal(t, int64(40), meta.Usage.CompletionTokens)
		})
	}
}

func cliContinuationJSON(t *testing.T, body, path string, value any) string {
	t.Helper()
	updated, err := sjson.Set(body, path, value)
	require.NoError(t, err)
	return updated
}

func cliContinuationChangeEvent(t *testing.T, event *httpclient.StreamEvent, path string, value any) *httpclient.StreamEvent {
	t.Helper()
	updated := *event
	var err error
	updated.Data, err = sjson.SetBytes(event.Data, path, value)
	require.NoError(t, err)
	return &updated
}

func TestClaudeCLIStreamContinuation_Scope(t *testing.T) {
	progress := "先检查仓库，再出工单。"
	tests := []struct {
		name, channel, userAgent, body, text string
		disabled, otherAPI                   bool
		calls                                int
	}{
		{name: "白名单执行请求", calls: 2},
		{name: "另一白名单渠道", channel: "linxi", calls: 2},
		{name: "普通客户端", userAgent: "sdk/1", calls: 1},
		{name: "非白名单", channel: "other", calls: 1},
		{name: "未启用恢复", disabled: true, calls: 1},
		{name: "非Messages协议", otherAPI: true, calls: 1},
		{name: "非流式", body: cliContinuationJSON(t, cliContinuationRequest, "stream", false), calls: 1},
		{name: "没有工具", body: cliContinuationJSON(t, cliContinuationRequest, "tools", []any{}), calls: 1},
		{name: "禁用工具", body: cliContinuationJSON(t, cliContinuationRequest, "tool_choice.type", "none"), calls: 1},
		{name: "自定义停止词", body: cliContinuationJSON(t, cliContinuationRequest, "stop_sequences", []string{"STOP"}), calls: 1},
		{name: "结构化输出", body: cliContinuationJSON(t, cliContinuationRequest, "output_config.format.type", "json_schema"), calls: 1},
		{name: "普通问答", body: cliContinuationJSON(t, cliContinuationRequest, "messages.0.content", "这是什么意思？"), calls: 1},
		{name: "明确短答", body: cliContinuationJSON(t, cliContinuationRequest, "messages.0.content", "请检查，只回复一句话"), calls: 1},
		{name: "只要方案", body: cliContinuationJSON(t, cliContinuationRequest, "messages.0.content", "帮我检查，只给方案，不要执行"), calls: 1},
		{name: "正常完成", text: "任务已完成，工单如下。", calls: 1},
		{name: "完成结果包含动作回顾", text: "我先检查了仓库，已经完成了。", calls: 1},
		{name: "助手预填充", body: cliContinuationJSON(t, cliContinuationRequest, "messages.1", map[string]any{"role": "assistant", "content": "先看"}), calls: 1},
		{name: "需要确认", text: "先检查仓库，请提供目标分支。", calls: 1},
		{name: "询问用户", text: "先检查仓库吗？", calls: 1},
		{name: "长回复", text: progress + strings.Repeat("检查结果。", 1000), calls: 1},
		{name: "代码块", text: "先检查仓库。\n```text\n结果\n```", calls: 1},
		{name: "英文执行", body: cliContinuationJSON(t, cliContinuationRequest, "messages.0.content", "Please fix the bug"), text: "I'll check the repository next.", calls: 2},
		{name: "工具结果后继续", body: cliContinuationJSON(t, cliContinuationRequest, "messages.1", map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool_1", "content": "ok"}}}), calls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel, ua, body, text := tt.channel, tt.userAgent, tt.body, tt.text
			if channel == "" {
				channel = "100x"
			}
			if ua == "" {
				ua = "claude-cli/2.1"
			}
			if body == "" {
				body = cliContinuationRequest
			}
			if text == "" {
				text = progress
			}
			selectors := []string{"100x", "linxi"}
			if tt.disabled {
				selectors = nil
			}
			outbound, request := cliContinuationOutbound(t, channel, ua, body, selectors)
			if tt.otherAPI {
				request.APIFormat = llm.APIFormatOpenAIChatCompletion.String()
			}
			executor := &cliContinuationTestExecutor{responses: [][]*httpclient.StreamEvent{cliContinuationTextEvents("msg_1", text), cliContinuationTextEvents("msg_2", "任务已完成。")}}
			stream, err := outbound.CustomizeExecutor(executor).DoStream(t.Context(), request)
			require.NoError(t, err)
			_, err = streams.All(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			require.NoError(t, stream.Err(), "正常 Close 不应成为取消错误")
			require.Len(t, executor.requests, tt.calls)
		})
	}
}

func TestClaudeCLIStreamContinuation_OnceAcrossRetries(t *testing.T) {
	outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
	executor := &cliContinuationTestExecutor{responses: [][]*httpclient.StreamEvent{cliContinuationTextEvents("msg_1", "先检查仓库，再出工单。")}}
	for attempt := 0; attempt < 2; attempt++ {
		request, err := guardClaudeCLIContinuation(outbound, biz.CompatibilityConfig{AnthropicStreamRecoveryChannels: []string{"100x"}}).OnOutboundRawRequest(t.Context(), request)
		require.NoError(t, err)
		stream, err := outbound.CustomizeExecutor(executor).DoStream(t.Context(), request)
		require.NoError(t, err)
		chunks, err := streams.All(stream)
		require.NoError(t, err)
		require.NoError(t, stream.Close())
		stops := 0
		for _, event := range chunks {
			if event.Type == "message_stop" {
				stops++
			}
		}
		require.Equal(t, 1, stops)
		require.Len(t, executor.requests, attempt+2, "第二段仍是进度句时也只能追加一次；重试不重置限额")
	}
	outbound.state.CurrentCandidate.Channel.Name = "other"
	_, err := guardClaudeCLIContinuation(outbound, biz.CompatibilityConfig{AnthropicStreamRecoveryChannels: []string{"100x"}}).OnOutboundRawRequest(t.Context(), request)
	require.NoError(t, err)
	require.False(t, outbound.claudeCLIStreamContinuation)
}

func TestClaudeCLIStreamContinuation_ThinkingAndToolIndexes(t *testing.T) {
	first := cliContinuationTextEvents("msg_first", "先检查仓库，再出工单。")
	thinking := []*httpclient.StreamEvent{
		anthropicRecoveryEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		anthropicRecoveryEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"检查计划"}}`),
		anthropicRecoveryEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"fixture-signature"}}`),
		anthropicRecoveryEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
	}
	for i := 1; i <= 3; i++ {
		first[i] = cliContinuationChangeEvent(t, first[i], "index", 1)
	}
	first = append(append(append([]*httpclient.StreamEvent{}, first[0]), thinking...), first[1:]...)
	final := []*httpclient.StreamEvent{
		cliContinuationTextEvents("msg_tool", "")[0],
		anthropicRecoveryEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_read","name":"Read","input":{}}}`),
		anthropicRecoveryEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`),
		anthropicRecoveryEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"fixture.txt\"}"}}`),
		anthropicRecoveryEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		anthropicRecoveryEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":25}}`),
		anthropicRecoveryEvent("message_stop", `{"type":"message_stop"}`),
	}
	outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
	executor := &cliContinuationTestExecutor{responses: [][]*httpclient.StreamEvent{first, final}}
	stream, err := outbound.CustomizeExecutor(executor).DoStream(t.Context(), request)
	require.NoError(t, err)
	defer stream.Close()
	chunks, err := streams.All(stream)
	require.NoError(t, err)
	require.Len(t, executor.requests, 2)
	require.NotContains(t, string(executor.requests[1].Body), "fixture-signature")
	aggregate, meta, err := anthropic.AggregateStreamChunks(t.Context(), chunks, anthropic.PlatformDirect)
	require.NoError(t, err)
	require.Equal(t, "msg_first", meta.ID)
	require.Equal(t, "tool_use", gjson.GetBytes(aggregate, "stop_reason").String())
	require.Equal(t, "fixture-signature", gjson.GetBytes(aggregate, "content.0.signature").String())
	require.Equal(t, "tool_read", gjson.GetBytes(aggregate, "content.2.id").String())
	require.Equal(t, "fixture.txt", gjson.GetBytes(aggregate, "content.2.input.path").String())
	require.Equal(t, int64(45), meta.Usage.CompletionTokens)
}

type cliContinuationErrorStream struct {
	streams.Stream[*httpclient.StreamEvent]
	err    error
	closed atomic.Int32
}

func (s *cliContinuationErrorStream) Err() error   { return s.err }
func (s *cliContinuationErrorStream) Close() error { s.closed.Add(1); return s.Stream.Close() }

func TestClaudeCLIStreamContinuation_UnsafeFirstSegment(t *testing.T) {
	for _, name := range []string{"未闭合块", "缺少开始", "缺少终止原因", "工具调用", "显式错误", "传输错误", "非法索引"} {
		t.Run(name, func(t *testing.T) {
			events := cliContinuationTextEvents("msg_first", "先检查仓库，再出工单。")
			var sourceErr error
			switch name {
			case "未闭合块":
				events = append(events[:3], events[4:]...)
			case "缺少开始":
				events = events[1:]
			case "缺少终止原因":
				events = append(events[:4], events[5:]...)
			case "工具调用":
				events[1] = cliContinuationChangeEvent(t, events[1], "content_block.type", "tool_use")
			case "显式错误":
				events = append(events, anthropicRecoveryEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`))
			case "传输错误":
				sourceErr = io.ErrUnexpectedEOF
			case "非法索引":
				events[1] = cliContinuationChangeEvent(t, events[1], "index", -1)
			}
			source := &cliContinuationErrorStream{Stream: streams.SliceStream(events), err: sourceErr}
			executor := &cliContinuationTestExecutor{onStream: func(context.Context, *httpclient.Request, int) (streams.Stream[*httpclient.StreamEvent], error) {
				return source, nil
			}}
			outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
			stream, err := outbound.CustomizeExecutor(executor).DoStream(t.Context(), request)
			require.NoError(t, err)
			chunks, err := streams.All(stream)
			require.ErrorIs(t, err, sourceErr)
			require.Equal(t, events, chunks)
			require.NoError(t, stream.Close())
			require.Len(t, executor.requests, 1)
		})
	}
}

func TestClaudeCLIStreamContinuation_FollowupFailureHasNoSuccessTerminal(t *testing.T) {
	for _, name := range []string{"连接失败", "空流", "无事件", "缺少开始", "缺少结束原因", "未闭合块", "显式错误", "结束后传输错误", "重复开始", "非法索引"} {
		t.Run(name, func(t *testing.T) {
			executor := &cliContinuationTestExecutor{onStream: func(_ context.Context, _ *httpclient.Request, index int) (streams.Stream[*httpclient.StreamEvent], error) {
				if index == 0 {
					return streams.SliceStream(cliContinuationTextEvents("msg_first", "先检查仓库，再出工单。")), nil
				}
				events := cliContinuationTextEvents("msg_final", "任务已完成。")
				var streamErr error
				switch name {
				case "连接失败":
					return nil, errors.New("fixture connection failed")
				case "空流":
					return nil, nil
				case "无事件":
					events = nil
				case "缺少开始":
					events = events[1:]
				case "缺少结束原因":
					events = append(events[:4], events[5:]...)
				case "未闭合块":
					events = append(events[:3], events[4:]...)
				case "显式错误":
					events = append(events[:4], anthropicRecoveryEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`))
				case "结束后传输错误":
					streamErr = io.ErrUnexpectedEOF
				case "重复开始":
					events = append(events[:1], events...)
				case "非法索引":
					events[1] = cliContinuationChangeEvent(t, events[1], "index", math.MaxInt)
				}
				return &cliContinuationErrorStream{Stream: streams.SliceStream(events), err: streamErr}, nil
			}}
			outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
			stream, err := outbound.CustomizeExecutor(executor).DoStream(t.Context(), request)
			require.NoError(t, err)
			defer stream.Close()
			chunks, err := streams.All(stream)
			require.Error(t, err)
			require.Len(t, executor.requests, 2)
			for _, event := range chunks {
				require.NotEqual(t, "message_stop", event.Type)
				require.Empty(t, gjson.GetBytes(event.Data, "delta.stop_reason").String())
			}
		})
	}
}

func TestClaudeCLIStreamContinuation_CloseUnblocksConnection(t *testing.T) {
	connected := make(chan struct{})
	finished := make(chan error, 1)
	first := &cliContinuationErrorStream{Stream: streams.SliceStream(cliContinuationTextEvents("msg_first", "先检查仓库，再出工单。"))}
	executor := &cliContinuationTestExecutor{onStream: func(ctx context.Context, _ *httpclient.Request, index int) (streams.Stream[*httpclient.StreamEvent], error) {
		if index == 0 {
			return first, nil
		}
		close(connected)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
	stream, err := outbound.CustomizeExecutor(executor).DoStream(t.Context(), request)
	require.NoError(t, err)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("读取流时 panic: %v", r)
				finished <- errors.New("panic")
			}
		}()
		_, err := streams.All(stream)
		finished <- err
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("续作连接未开始")
	}
	require.NoError(t, stream.Close())
	require.NoError(t, stream.Close())
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("Close 未解除续作连接阻塞")
	}
	require.Equal(t, int32(1), first.closed.Load())
	require.Len(t, executor.requests, 2)
}

func TestClaudeCLIStreamContinuation_RequestAndUsageBounds(t *testing.T) {
	request := &httpclient.Request{Body: []byte(cliContinuationJSON(t, cliContinuationRequest, "metadata.fixture_integer", json.RawMessage("9007199254740993"))), Headers: http.Header{"Content-Length": []string{"1"}, "X-Fixture": []string{"kept"}}, JSONBody: []byte("old")}
	usage := map[string]json.RawMessage{"output_tokens": json.RawMessage("20")}
	next := claudeBuildContinuationRequest(request, "先检查仓库，再出工单。", usage)
	require.NotNil(t, next)
	require.Equal(t, "9007199254740993", gjson.GetBytes(next.Body, "metadata.fixture_integer").Raw)
	require.Equal(t, "ephemeral", gjson.GetBytes(next.Body, "system.0.cache_control.type").String())
	require.Equal(t, "kept", next.Headers.Get("X-Fixture"))
	require.Empty(t, next.Headers.Get("Content-Length"))
	require.Nil(t, next.JSONBody)
	require.Equal(t, "1", request.Headers.Get("Content-Length"))
	require.Equal(t, []byte("old"), request.JSONBody)
	for _, tt := range []struct{ max, spent string }{{"-9223372036854775808", "9223372036854775807"}, {"-1", "20"}, {"1024", "-1"}, {"1024", "1025"}, {"1024", "900"}, {"1024", "null"}, {"1024", "1.5"}, {"1024.5", "20"}} {
		t.Run(tt.max+"_"+tt.spent, func(t *testing.T) {
			body, err := sjson.SetRawBytes(request.Body, "max_tokens", []byte(tt.max))
			require.NoError(t, err)
			require.Nil(t, claudeBuildContinuationRequest(&httpclient.Request{Body: body}, "先检查", map[string]json.RawMessage{"output_tokens": json.RawMessage(tt.spent)}))
		})
	}
	thinking := cliContinuationJSON(t, cliContinuationRequest, "thinking.budget_tokens", 1004)
	require.Nil(t, claudeBuildContinuationRequest(&httpclient.Request{Body: []byte(thinking)}, "先检查", usage))
	previous := make(map[string]json.RawMessage)
	mergeClaudeUsage(previous, `{"input_tokens":100,"output_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":11},"service_tier":"standard"}`)
	mergeClaudeUsage(previous, `{"cache_creation":{"ephemeral_5m_input_tokens":9}}`)
	current := make(map[string]json.RawMessage)
	mergeClaudeUsage(current, `{"input_tokens":150,"output_tokens":25,"cache_creation":{"ephemeral_1h_input_tokens":13},"service_tier":"standard"}`)
	total, err := addClaudeUsage(previous, current)
	require.NoError(t, err)
	encoded, err := json.Marshal(total)
	require.NoError(t, err)
	require.Equal(t, int64(45), gjson.GetBytes(encoded, "output_tokens").Int())
	require.Equal(t, int64(9), gjson.GetBytes(encoded, "cache_creation.ephemeral_5m_input_tokens").Int())
	require.Equal(t, int64(24), gjson.GetBytes(encoded, "cache_creation.ephemeral_1h_input_tokens").Int())
	require.Equal(t, "standard", gjson.GetBytes(encoded, "service_tier").String())
	_, err = addClaudeUsage(map[string]json.RawMessage{"output_tokens": json.RawMessage("9223372036854775807")}, usage)
	require.Error(t, err)
}
