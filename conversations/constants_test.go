package conversations

import "testing"

func TestLookupConstant(t *testing.T) {
	tests := []struct {
		name string
		key  ConstantKey
		want string
	}{
		{
			name: "retry prompt text",
			key:  RetryPromptTextKey,
			want: "Continue with your prompt",
		},
		{
			name: "continuation prompt text",
			key:  ContinuationPromptTextKey,
			want: "Continue. Remember to output the completion marker <complete /> only if you have finished.",
		},
		{
			name: "provider session key format",
			key:  ProviderSessionKeyFormatKey,
			want: "session key:agentid:file_name:openclaw",
		},
		{
			name: "provider model",
			key:  ProviderModelKey,
			want: "openclaw",
		},
		{
			name: "conversation session key template",
			key:  ConversationSessionKeyTemplateKey,
			want: "agent:%s:guava:%s",
		},
		{
			name: "default account instructions",
			key:  InstructionsKey,
			want: "Output <complete /> only when finished.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := LookupConstant(test.key)
			if !ok {
				t.Fatal("LookupConstant() reported the key missing")
			}
			if got != test.want {
				t.Errorf("LookupConstant() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLookupConstantUnknownKey(t *testing.T) {
	if got, ok := LookupConstant("unknown"); ok || got != "" {
		t.Errorf("LookupConstant(unknown) = %q, %t; want empty, false", got, ok)
	}
}
