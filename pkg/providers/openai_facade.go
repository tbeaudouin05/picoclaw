package providers

import openaiapi "github.com/sipeed/picoclaw/pkg/providers/openai"

// OpenAIProvider is the API-key authenticated official OpenAI Responses API
// provider. OpenAI-compatible custom api_base entries continue to use
// HTTPProvider instead.
type OpenAIProvider = openaiapi.Provider

func NewOpenAIProvider(
	apiKey, apiBase, proxy, userAgent string,
	opts ...openaiapi.Option,
) *OpenAIProvider {
	return openaiapi.NewProvider(apiKey, apiBase, proxy, userAgent, opts...)
}

func NewOpenAIProviderWithTimeout(
	apiKey, apiBase, proxy, userAgent string,
	requestTimeoutSeconds int,
	extraBody map[string]any,
	customHeaders map[string]string,
) *OpenAIProvider {
	return openaiapi.NewProviderWithTimeout(
		apiKey,
		apiBase,
		proxy,
		userAgent,
		requestTimeoutSeconds,
		extraBody,
		customHeaders,
	)
}
