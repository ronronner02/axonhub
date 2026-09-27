package biz

import "testing"

func TestAnthropicStreamRecoveryEnabledFor(t *testing.T) {
	t.Parallel()

	config := CompatibilityConfig{
		AnthropicStreamRecoveryChannels: []string{" linxi ", "18"},
	}

	for _, tt := range []struct {
		name        string
		channelID   int
		channelName string
		want        bool
	}{
		{name: "按名称匹配", channelID: 17, channelName: "linxi", want: true},
		{name: "名称忽略大小写", channelID: 17, channelName: "LINXI", want: true},
		{name: "按数字 ID 匹配", channelID: 18, channelName: "renamed", want: true},
		{name: "未配置渠道", channelID: 19, channelName: "other", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := config.AnthropicStreamRecoveryEnabledFor(tt.channelID, tt.channelName); got != tt.want {
				t.Fatalf("AnthropicStreamRecoveryEnabledFor(%d, %q) = %v, want %v", tt.channelID, tt.channelName, got, tt.want)
			}
		})
	}
}

func TestResponsesOpaque400RecoveryEnabledFor(t *testing.T) {
	t.Parallel()

	config := CompatibilityConfig{
		ResponsesOpaque400RecoveryChannels: []string{" any2-gpt ", "18"},
	}

	for _, tt := range []struct {
		name        string
		channelID   int
		channelName string
		want        bool
	}{
		{name: "按名称匹配", channelID: 17, channelName: "any2-gpt", want: true},
		{name: "名称忽略大小写", channelID: 17, channelName: "ANY2-GPT", want: true},
		{name: "按数字 ID 匹配", channelID: 18, channelName: "renamed", want: true},
		{name: "未配置渠道", channelID: 19, channelName: "other", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := config.ResponsesOpaque400RecoveryEnabledFor(tt.channelID, tt.channelName); got != tt.want {
				t.Fatalf("ResponsesOpaque400RecoveryEnabledFor(%d, %q) = %v, want %v", tt.channelID, tt.channelName, got, tt.want)
			}
		})
	}
}
