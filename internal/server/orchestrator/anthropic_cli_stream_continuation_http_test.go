package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
)

type cliContinuationCustomizedOutbound struct {
	transformer.Outbound
	calls atomic.Int32
}

func (o *cliContinuationCustomizedOutbound) CustomizeExecutor(executor pipeline.Executor) pipeline.Executor {
	return &cliContinuationMarkerExecutor{Executor: executor, calls: &o.calls}
}

type cliContinuationMarkerExecutor struct {
	pipeline.Executor
	calls *atomic.Int32
}

func (e *cliContinuationMarkerExecutor) DoStream(ctx context.Context, request *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	e.calls.Add(1)
	copy := *request
	copy.Headers = request.Headers.Clone()
	copy.Headers.Set("X-Fixture-Customized", "kept")
	return e.Executor.DoStream(ctx, &copy)
}

func cliContinuationWriteSSE(w http.ResponseWriter, events []*httpclient.StreamEvent) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, event.Data)
		w.(http.Flusher).Flush()
	}
}

func TestClaudeCLIStreamContinuation_HTTPPipeline(t *testing.T) {
	for _, passThrough := range []bool{false, true} {
		for _, missingStop := range []string{"none", "first", "followup"} {
			t.Run(fmt.Sprintf("passthrough=%v/missing_stop=%s", passThrough, missingStop), func(t *testing.T) {
				first := cliContinuationTextEvents("msg_first", "先看仓库，再出今天工单。")
				final := cliContinuationTextEvents("msg_final", "任务已完成，工单如下。")
				first[0] = cliContinuationChangeEvent(t, first[0], "message.usage.cache_creation_input_tokens", 30)
				first[0] = cliContinuationChangeEvent(t, first[0], "message.usage.cache_creation", map[string]int{"ephemeral_5m_input_tokens": 10, "ephemeral_1h_input_tokens": 20})
				final[0] = cliContinuationChangeEvent(t, final[0], "message.usage.cache_creation_input_tokens", 70)
				final[0] = cliContinuationChangeEvent(t, final[0], "message.usage.cache_creation", map[string]int{"ephemeral_5m_input_tokens": 30, "ephemeral_1h_input_tokens": 40})
				if missingStop == "first" {
					first = first[:len(first)-1]
				}
				if missingStop == "followup" {
					final = final[:len(final)-1]
				}
				var mu sync.Mutex
				var bodies [][]byte
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("读取 fixture 请求: %v", err)
						http.Error(w, "read error", 500)
						return
					}
					if r.Header.Get("X-Fixture-Customized") != "kept" || r.Header.Get("X-Fixture-Auth") != "fixture-auth" {
						t.Error("渠道 executor 或认证头未保留")
					}
					mu.Lock()
					index := len(bodies)
					bodies = append(bodies, body)
					mu.Unlock()
					if index == 0 {
						cliContinuationWriteSSE(w, first)
					} else {
						cliContinuationWriteSSE(w, final)
					}
				}))
				defer upstream.Close()
				outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
				request.URL = upstream.URL
				request.Method = http.MethodPost
				request.Headers.Set("Content-Type", "application/json")
				request.Headers.Set("X-Fixture-Auth", "fixture-auth")
				wrapped, err := anthropic.NewOutboundTransformer(upstream.URL, "fixture-key")
				require.NoError(t, err)
				customized := &cliContinuationCustomizedOutbound{Outbound: wrapped}
				outbound.wrapped = customized
				channel := outbound.GetCurrentChannel()
				channel.HTTPClient = httpclient.NewHttpClientWithClient(upstream.Client())
				channel.Settings = &objects.ChannelSettings{PassThroughBody: lo.ToPtr(passThrough)}
				outbound.state.LlmRequest.APIFormat = llm.APIFormatAnthropicMessage
				outbound.state.LlmRequest.Stream = lo.ToPtr(true)
				outbound.state.OriginalRequestStream = lo.ToPtr(true)
				service := &biz.SystemService{CompatibilityConfig: biz.CompatibilityConfig{AnthropicStreamRecoveryChannels: []string{"100x"}}}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				// 使用生产的 executor、恢复、fanout、格式转换与透传中间件，fixture 只替代上游 HTTP。
				raw, err := outbound.CustomizeExecutor(&cliContinuationTestExecutor{}).DoStream(ctx, request)
				require.NoError(t, err)
				defer raw.Close()
				middlewares := rawProviderCaptureMiddlewares(outbound, service)
				for i := len(middlewares) - 1; i >= 0; i-- {
					raw, err = middlewares[i].OnOutboundRawStream(ctx, raw)
					require.NoError(t, err)
				}
				normalized, err := wrapped.TransformStream(ctx, request, raw)
				require.NoError(t, err)
				converted, err := anthropic.NewInboundTransformer().TransformStream(ctx, normalized)
				require.NoError(t, err)
				clientStream, err := applyPassThroughStream(outbound, service).OnInboundRawStream(ctx, converted)
				require.NoError(t, err)
				chunks, err := streams.All(clientStream)
				require.NoError(t, err)
				require.NoError(t, clientStream.Close())
				require.NoError(t, clientStream.Err(), "fanout 正常结束不应变成 context canceled")
				require.Equal(t, int32(2), customized.calls.Load())
				mu.Lock()
				requests := append([][]byte(nil), bodies...)
				mu.Unlock()
				require.Len(t, requests, 2, "客户端的一次流请求应在 AxonHub 内续作")
				require.Equal(t, int64(1004), gjson.GetBytes(requests[1], "max_tokens").Int())
				starts, stops, terminals := 0, 0, 0
				for _, event := range chunks {
					switch event.Type {
					case "message_start":
						starts++
					case "message_stop":
						stops++
					case "message_delta":
						if gjson.GetBytes(event.Data, "delta.stop_reason").String() != "" {
							terminals++
						}
					}
				}
				require.Equal(t, 1, starts)
				require.Equal(t, 1, stops)
				require.Equal(t, 1, terminals)
				aggregate, meta, err := anthropic.AggregateStreamChunks(ctx, chunks, anthropic.PlatformDirect)
				require.NoError(t, err)
				require.Equal(t, "msg_first", meta.ID)
				require.Contains(t, string(aggregate), "任务已完成，工单如下。")
				require.Equal(t, "end_turn", gjson.GetBytes(aggregate, "stop_reason").String())
				require.Equal(t, int64(40), meta.Usage.CompletionTokens)
				require.Equal(t, int64(340), meta.Usage.PromptTokens)
				require.Equal(t, int64(40), meta.Usage.PromptTokensDetails.WriteCached5MinTokens)
				require.Equal(t, int64(60), meta.Usage.PromptTokensDetails.WriteCached1HourTokens)
			})
		}
	}
}

func TestClaudeCLIStreamContinuation_DoesNotBufferFirstContent(t *testing.T) {
	allowFinish := make(chan struct{})
	var calls atomic.Int32
	first := cliContinuationTextEvents("msg_first", "先检查仓库，再出工单。")
	final := cliContinuationTextEvents("msg_final", "任务已完成。")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			cliContinuationWriteSSE(w, first[:3])
			select {
			case <-allowFinish:
				cliContinuationWriteSSE(w, first[3:])
			case <-r.Context().Done():
			}
		} else {
			cliContinuationWriteSSE(w, final)
		}
	}))
	defer upstream.Close()
	outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
	request.URL = upstream.URL
	request.Method = http.MethodPost
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := outbound.CustomizeExecutor(httpclient.NewHttpClientWithClient(upstream.Client())).DoStream(ctx, request)
	require.NoError(t, err)
	defer stream.Close()
	for range 3 {
		require.True(t, stream.Next(), "应在上游结束前交付首段内容")
		require.NotNil(t, stream.Current())
	}
	require.Equal(t, int32(1), calls.Load())
	close(allowFinish)
	_, err = streams.All(stream)
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
}

func TestClaudeCLIStreamContinuation_HTTPCancellation(t *testing.T) {
	for _, action := range []string{"close", "cancel"} {
		t.Run(action, func(t *testing.T) {
			following := make(chan struct{})
			upstreamCanceled := make(chan struct{})
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					cliContinuationWriteSSE(w, cliContinuationTextEvents("msg_first", "先检查仓库，再出工单。"))
					return
				}
				cliContinuationWriteSSE(w, cliContinuationTextEvents("msg_final", "正在检查")[0:3])
				close(following)
				<-r.Context().Done()
				close(upstreamCanceled)
			}))
			defer upstream.Close()
			outbound, request := cliContinuationOutbound(t, "100x", "claude-cli/2.1", cliContinuationRequest, []string{"100x"})
			request.URL = upstream.URL
			request.Method = http.MethodPost
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream, err := outbound.CustomizeExecutor(httpclient.NewHttpClientWithClient(upstream.Client())).DoStream(ctx, request)
			require.NoError(t, err)
			defer stream.Close()
			result := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("读取流时 panic: %v", r)
						result <- errors.New("panic")
					}
				}()
				_, err := streams.All(stream)
				result <- err
			}()
			select {
			case <-following:
			case <-time.After(3 * time.Second):
				t.Fatal("第二段 HTTP 流未开始")
			}
			if action == "close" {
				require.NoError(t, stream.Close())
			} else {
				cancel()
			}
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(3 * time.Second):
				t.Fatal("读取未响应取消")
			}
			select {
			case <-upstreamCanceled:
			case <-time.After(3 * time.Second):
				t.Fatal("上游连接未释放")
			}
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestClaudeCLIStreamContinuation_ProgressClassifier(t *testing.T) {
	for _, text := range []string{"先检查仓库，再出工单。", "I'll check the repository next."} {
		require.True(t, claudeProgressOnly(text))
	}
	for _, text := range []string{"", "答案：先检查仓库。", "已修复；接下来看日志。", strings.Repeat("先检查。", 300)} {
		require.False(t, claudeProgressOnly(text))
	}
}
