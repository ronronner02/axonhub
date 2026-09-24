package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestGuardClaudeCLIContinuation(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		channelID  int
		channel    string
		userAgent  string
		apiFormat  llm.APIFormat
		body       string
		selectors  []string
		wantChange bool
	}{
		{name: "100x Claude Code", channelID: 18, channel: "100x", userAgent: "claude-cli/2.1.281 (external, sdk-cli)", apiFormat: llm.APIFormatAnthropicMessage, body: `{"system":[{"type":"text","text":"keep","cache_control":{"type":"ephemeral"}}],"messages":[]}`, selectors: []string{"100x"}, wantChange: true},
		{name: "linxi Claude Code", channelID: 17, channel: "linxi", userAgent: "claude-cli/2.1.281", apiFormat: llm.APIFormatAnthropicMessage, body: `{"system":[{"type":"text","text":"keep"}]}`, selectors: []string{"linxi"}, wantChange: true},
		{name: "Pi 保持原样", channelID: 18, channel: "100x", userAgent: "pi/1.0", apiFormat: llm.APIFormatAnthropicMessage, body: `{"system":[{"type":"text","text":"keep"}]}`, selectors: []string{"100x"}},
		{name: "其他渠道保持原样", channelID: 19, channel: "other", userAgent: "claude-cli/2.1.281", apiFormat: llm.APIFormatAnthropicMessage, body: `{"system":[{"type":"text","text":"keep"}]}`, selectors: []string{"100x"}},
		{name: "未配置时保持原样", channelID: 18, channel: "100x", userAgent: "claude-cli/2.1.281", apiFormat: llm.APIFormatAnthropicMessage, body: `{"system":[{"type":"text","text":"keep"}]}`},
		{name: "其他协议保持原样", channelID: 18, channel: "100x", userAgent: "claude-cli/2.1.281", apiFormat: llm.APIFormatOpenAIChatCompletion, body: `{"system":[{"type":"text","text":"keep"}]}`, selectors: []string{"100x"}},
		{name: "非数组 system 保持原样", channelID: 18, channel: "100x", userAgent: "claude-cli/2.1.281", apiFormat: llm.APIFormatAnthropicMessage, body: `{"system":"keep"}`, selectors: []string{"100x"}},
		{name: "无 system 保持原样", channelID: 18, channel: "100x", userAgent: "claude-cli/2.1.281", apiFormat: llm.APIFormatAnthropicMessage, body: `{"messages":[]}`, selectors: []string{"100x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := &PersistenceState{
				CurrentCandidate: &ChannelModelsCandidate{Channel: &biz.Channel{Channel: &ent.Channel{ID: tt.channelID, Name: tt.channel}}},
				LlmRequest:       &llm.Request{RawRequest: &httpclient.Request{Headers: http.Header{"User-Agent": []string{tt.userAgent}}}},
			}
			outbound := &PersistentOutboundTransformer{state: state}
			original := &httpclient.Request{APIFormat: tt.apiFormat.String(), Body: []byte(tt.body), JSONBody: []byte(tt.body), Headers: http.Header{"User-Agent": []string{"axonhub/1.0"}}}
			state.RawProviderRequest = original
			middleware := guardClaudeCLIContinuation(outbound, biz.CompatibilityConfig{AnthropicStreamRecoveryChannels: tt.selectors})
			got, err := middleware.OnOutboundRawRequest(context.Background(), original)
			require.NoError(t, err)
			if !tt.wantChange {
				require.Same(t, original, got)
				require.Equal(t, tt.body, string(got.Body))
				return
			}
			require.NotSame(t, original, got)
			require.Equal(t, tt.body, string(original.Body))
			require.Nil(t, got.JSONBody)
			require.Same(t, got, state.RawProviderRequest)
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(got.Body, &body))
			var system []map[string]any
			require.NoError(t, json.Unmarshal(body["system"], &system))
			require.Len(t, system, 2)
			require.Equal(t, "keep", system[0]["text"])
			require.Equal(t, claudeCLIContinuationInstruction, system[1]["text"])
			if tt.channelID == 18 {
				require.Equal(t, "ephemeral", system[0]["cache_control"].(map[string]any)["type"])
			}
		})
	}
}

func TestIsClaudeCLIRequest_MissingInboundRequest(t *testing.T) {
	t.Parallel()

	require.False(t, isClaudeCLIRequest(nil))
	require.False(t, isClaudeCLIRequest(&llm.Request{}))
	require.False(t, isClaudeCLIRequest(&llm.Request{RawRequest: &httpclient.Request{}}))
}
