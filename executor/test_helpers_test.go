package executor

const testCompletionMarker = "<complete />"

type testResponsesAPIResponse struct {
	ID     string                    `json:"id"`
	Output []testResponsesOutputItem `json:"output"`
}

type testResponsesOutputItem struct {
	Type    string                     `json:"type"`
	Role    string                     `json:"role"`
	Content []testResponsesContentPart `json:"content"`
}

type testResponsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func testAssistantText(parts ...string) []testResponsesOutputItem {
	content := make([]testResponsesContentPart, 0, len(parts))
	for _, part := range parts {
		content = append(content, testResponsesContentPart{Type: "output_text", Text: part})
	}
	return []testResponsesOutputItem{{Type: "message", Role: "assistant", Content: content}}
}
