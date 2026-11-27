package conversations

// ConstantKey identifies a stable value in the conversations constants table.
type ConstantKey string

const (
	// RetryPromptTextKey identifies the retry prompt shown to the provider.
	RetryPromptTextKey ConstantKey = "retry_prompt_text"
	// ProviderSessionKeyFormatKey identifies the documented provider session-key format.
	ProviderSessionKeyFormatKey ConstantKey = "provider_session_key_format"
	// ProviderModelKey identifies the model name used for OpenResponses requests.
	ProviderModelKey ConstantKey = "provider_model"
)

const (
	retryPromptText          = "Continue with your prompt"
	providerSessionKeyFormat = "session key:agentid:file_name:openclaw"
	providerModel            = "openclaw"
)

// constantsTable is initialized with the conversations package and remains
// available through LookupConstant for the lifetime of the process.
var constantsTable = map[ConstantKey]string{
	RetryPromptTextKey:          retryPromptText,
	ProviderSessionKeyFormatKey: providerSessionKeyFormat,
	ProviderModelKey:            providerModel,
}

// LookupConstant returns a stable configured value and whether key is present.
func LookupConstant(key ConstantKey) (string, bool) {
	value, ok := constantsTable[key]
	return value, ok
}
