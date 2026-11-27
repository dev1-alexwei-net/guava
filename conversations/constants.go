package conversations

// ConstantKey identifies a stable value in the conversations constants table.
type ConstantKey string

const (
	// RetryPromptTextKey identifies the retry prompt shown to the provider.
	RetryPromptTextKey ConstantKey = "retry_prompt_text"
	// ContinuationPromptTextKey identifies the prompt used to continue an incomplete response.
	ContinuationPromptTextKey ConstantKey = "continuation_prompt_text"
	// ProviderSessionKeyFormatKey identifies the documented provider session-key format.
	ProviderSessionKeyFormatKey ConstantKey = "provider_session_key_format"
	// ProviderModelKey identifies the model name used for OpenResponses requests.
	ProviderModelKey ConstantKey = "provider_model"
	// ConversationSessionKeyTemplateKey identifies the runtime session-key template.
	ConversationSessionKeyTemplateKey ConstantKey = "conversation_session_key_template"
	// InstructionsKey identifies the suggested default account instructions.
	InstructionsKey ConstantKey = "instructions"
)

const (
	retryPromptText                = "Continue with your prompt"
	continuationPromptText         = "Continue. Remember to output the completion marker <complete /> only if you have finished."
	providerSessionKeyFormat       = "session key:agentid:file_name:openclaw"
	providerModel                  = "openclaw"
	conversationSessionKeyTemplate = "agent:%s:guava:%s"
	defaultInstructions            = "Output <complete /> only when finished."
)

// constantsTable is initialized with the conversations package and remains
// available through LookupConstant for the lifetime of the process.
var constantsTable = map[ConstantKey]string{
	RetryPromptTextKey:                retryPromptText,
	ContinuationPromptTextKey:         continuationPromptText,
	ProviderSessionKeyFormatKey:       providerSessionKeyFormat,
	ProviderModelKey:                  providerModel,
	ConversationSessionKeyTemplateKey: conversationSessionKeyTemplate,
	InstructionsKey:                   defaultInstructions,
}

// LookupConstant returns a stable configured value and whether key is present.
func LookupConstant(key ConstantKey) (string, bool) {
	value, ok := constantsTable[key]
	return value, ok
}
