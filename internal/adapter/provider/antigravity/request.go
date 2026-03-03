package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// RequestConfig holds resolved request configuration (like Antigravity-Manager)
type RequestConfig struct {
	RequestType        string // "agent", "web_search", or "image_gen"
	FinalModel         string
	InjectGoogleSearch bool
	ImageConfig        map[string]interface{} // Image generation config (if request_type is image_gen)
}

// isStreamRequest checks if the request body indicates streaming
func isStreamRequest(body []byte) bool {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	stream, _ := req["stream"].(bool)
	return stream
}

// extractSessionID extracts metadata.user_id from request body for use as sessionId
// (like Antigravity-Manager's sessionId support)
func extractSessionID(body []byte) string {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	metadata, ok := req["metadata"].(map[string]interface{})
	if !ok {
		return ""
	}

	userID, _ := metadata["user_id"].(string)
	return userID
}

// unwrapGeminiCLIEnvelope extracts the inner request from Gemini CLI envelope format
// Gemini CLI sends: {"request": {...}, "model": "..."}
// Gemini API expects just the inner request content
func unwrapGeminiCLIEnvelope(body []byte) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	if innerRequest, ok := data["request"]; ok {
		if unwrapped, err := json.Marshal(innerRequest); err == nil {
			return unwrapped
		}
	}

	return body
}

// resolveRequestConfig determines request type and final model name
// (like Antigravity-Manager's resolve_request_config)
func resolveRequestConfig(originalModel, mappedModel string, tools []interface{}) RequestConfig {
	// 1. Image Generation Check (Priority)
	if strings.HasPrefix(mappedModel, "gemini-3-pro-image") {
		imageConfig, cleanModel := ParseImageConfig(originalModel)
		return RequestConfig{
			RequestType: "image_gen",
			FinalModel:  cleanModel,
			ImageConfig: imageConfig,
		}
	}

	// Check for -online suffix
	isOnlineSuffix := strings.HasSuffix(originalModel, "-online")

	// Check for networking tools in the request
	hasNetworkingTool := detectsNetworkingTool(tools)

	// Strip -online suffix from final model
	finalModel := strings.TrimSuffix(mappedModel, "-online")

	// Determine if we should enable networking
	enableNetworking := isOnlineSuffix || hasNetworkingTool

	// If networking enabled, force gemini-2.5-flash (only model that supports googleSearch)
	if enableNetworking && finalModel != "gemini-2.5-flash" {
		finalModel = "gemini-2.5-flash"
	}

	requestType := "agent"
	if enableNetworking {
		requestType = "web_search"
	}

	return RequestConfig{
		RequestType:        requestType,
		FinalModel:         finalModel,
		InjectGoogleSearch: enableNetworking,
	}
}

// detectsNetworkingTool checks if tool list contains networking/web search tools.
// Mirrors Antigravity-Manager's `detects_networking_tool`.
func detectsNetworkingTool(tools []interface{}) bool {
	if len(tools) == 0 {
		return false
	}

	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}

		// 1) Direct style: { "name": "..." } or { "type": "..." }
		if name, _ := toolMap["name"].(string); name != "" {
			switch name {
			case "web_search", "google_search", "web_search_20250305", "google_search_retrieval":
				return true
			}
		}
		if t, _ := toolMap["type"].(string); t != "" {
			switch t {
			case "web_search", "google_search", "web_search_20250305", "google_search_retrieval":
				return true
			}
		}

		// 2) OpenAI nested style: { "type": "function", "function": { "name": "..." } }
		if fn, ok := toolMap["function"].(map[string]interface{}); ok {
			if fnName, _ := fn["name"].(string); fnName != "" {
				switch fnName {
				case "web_search", "google_search", "web_search_20250305", "google_search_retrieval":
					return true
				}
			}
		}

		// 3) Gemini tool declarations: { "functionDeclarations": [ { "name": "..." } ] }
		if decls, ok := toolMap["functionDeclarations"].([]interface{}); ok {
			for _, decl := range decls {
				declMap, ok := decl.(map[string]interface{})
				if !ok {
					continue
				}
				if name, _ := declMap["name"].(string); name != "" {
					switch name {
					case "web_search", "google_search", "google_search_retrieval":
						return true
					}
				}
			}
		}

		// 4) Gemini googleSearch declarations
		if _, ok := toolMap["googleSearch"]; ok {
			return true
		}
		if _, ok := toolMap["googleSearchRetrieval"]; ok {
			return true
		}
	}

	return false
}

// wrapV1InternalRequest wraps the request body in v1internal format
// Similar to Antigravity-Manager's wrap_request function
func wrapV1InternalRequest(body []byte, projectID, originalModel, mappedModel, sessionID string, toolsForConfig []interface{}) ([]byte, error) {
	var innerRequest map[string]interface{}
	if err := json.Unmarshal(body, &innerRequest); err != nil {
		return nil, err
	}

	// Remove model field from inner request if present (will be at top level)
	delete(innerRequest, "model")

	// Resolve request configuration (like Antigravity-Manager)
	toolsForDetection := toolsForConfig
	if toolsForDetection == nil {
		if tools, ok := innerRequest["tools"].([]interface{}); ok {
			toolsForDetection = tools
		}
	}
	config := resolveRequestConfig(originalModel, mappedModel, toolsForDetection)

	// Inject googleSearch if needed and no function declarations present
	if config.InjectGoogleSearch {
		injectGoogleSearchTool(innerRequest)
	}

	// Handle imageConfig for image generation models (like Antigravity-Manager)
	if config.ImageConfig != nil {
		// 1. Remove tools (image generation does not support tools)
		delete(innerRequest, "tools")
		// 2. Remove systemInstruction (image generation does not support system prompts)
		delete(innerRequest, "systemInstruction")
		// 3. Clean generationConfig and inject imageConfig
		if genConfig, ok := innerRequest["generationConfig"].(map[string]interface{}); ok {
			delete(genConfig, "thinkingConfig")
			delete(genConfig, "responseMimeType")
			delete(genConfig, "responseModalities")
			genConfig["imageConfig"] = config.ImageConfig
		} else {
			innerRequest["generationConfig"] = map[string]interface{}{
				"imageConfig": config.ImageConfig,
			}
		}
	}

	// Deep clean [undefined] strings (Cherry Studio client common injection)
	deepCleanUndefined(innerRequest)

	// [Safety Settings] Inject safety settings from environment variable (like Antigravity-Manager)
	safetyThreshold := GetSafetyThresholdFromEnv()
	innerRequest["safetySettings"] = BuildSafetySettingsMap(safetyThreshold)

	// [SessionID Support] If metadata.user_id was provided, use it as sessionId (like Antigravity-Manager)
	if sessionID != "" {
		innerRequest["sessionId"] = sessionID
	}

	// Generate UUID requestId (like Antigravity-Manager)
	requestID := fmt.Sprintf("agent-%s", uuid.New().String())

	wrapped := map[string]interface{}{
		"project":     projectID,
		"requestId":   requestID,
		"request":     innerRequest,
		"model":       config.FinalModel,
		"userAgent":   "antigravity",
		"requestType": config.RequestType,
	}

	return json.Marshal(wrapped)
}

// stripThinkingFromClaude removes thinking config and blocks to retry without thinking (like Manager 400 retry)
func stripThinkingFromClaude(body []byte) []byte {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	// Remove thinking config
	delete(req, "thinking")

	// Clean model suffix
	if model, ok := req["model"].(string); ok {
		req["model"] = strings.ReplaceAll(model, "-thinking", "")
	}

	// Remove thinking/redacted_thinking blocks from messages
	if messages, ok := req["messages"].([]interface{}); ok {
		for i, msg := range messages {
			msgMap, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			content, ok := msgMap["content"].([]interface{})
			if !ok {
				continue
			}
			var filtered []interface{}
			for _, c := range content {
				if block, ok := c.(map[string]interface{}); ok {
					if t, ok := block["type"].(string); ok {
						if t == "thinking" || t == "redacted_thinking" {
							continue
						}
					}
				}
				filtered = append(filtered, c)
			}
			msgMap["content"] = filtered
			messages[i] = msgMap
		}
		req["messages"] = messages
	}

	data, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return data
}

// extractModelFromBody extracts model from a Claude request body
func extractModelFromBody(body []byte) string {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	if model, ok := req["model"].(string); ok {
		return model
	}
	return ""
}

// deepCleanUndefined recursively removes [undefined] strings from request body
// (like Antigravity-Manager's deep_clean_undefined)
func deepCleanUndefined(data map[string]interface{}) {
	for key, val := range data {
		if s, ok := val.(string); ok && s == "[undefined]" {
			delete(data, key)
			continue
		}
		if nested, ok := val.(map[string]interface{}); ok {
			deepCleanUndefined(nested)
		}
		if arr, ok := val.([]interface{}); ok {
			var filtered []interface{}
			for _, item := range arr {
				// Drop literal "[undefined]" items
				if s, ok := item.(string); ok && s == "[undefined]" {
					continue
				}
				if m, ok := item.(map[string]interface{}); ok {
					deepCleanUndefined(m)
				}
				filtered = append(filtered, item)
			}
			data[key] = filtered
		}
	}
}

func firstNRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func matchesAnyKeyword(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func extractLastUserMessageForBackgroundDetection(messages []interface{}) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		var content string
		switch c := msg["content"].(type) {
		case string:
			content = c
		case []interface{}:
			var texts []string
			for _, b := range c {
				bm, ok := b.(map[string]interface{})
				if !ok {
					continue
				}
				if t, _ := bm["type"].(string); t != "text" {
					continue
				}
				if text, ok := bm["text"].(string); ok {
					texts = append(texts, text)
				}
			}
			content = strings.Join(texts, " ")
		}

		if strings.TrimSpace(content) == "" ||
			strings.HasPrefix(content, "Warmup") ||
			strings.Contains(content, "<system-reminder>") {
			continue
		}

		return content
	}

	return ""
}

// detectBackgroundTask checks the latest meaningful user message for background-task keywords.
// Returns (true, forcedModel, modifiedBody) when detected, with tools/thinking stripped and thinking blocks removed.
// Mirrors Antigravity-Manager's background task detection logic.
func detectBackgroundTask(body []byte) (bool, string, []byte) {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return false, "", body
	}

	messages, ok := req["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return false, "", body
	}

	lastUserText := extractLastUserMessageForBackgroundDetection(messages)
	if lastUserText == "" {
		return false, "", body
	}

	// Background tasks are typically short; skip if too long
	if len(lastUserText) > 800 {
		return false, "", body
	}

	preview := firstNRunes(lastUserText, 500)

	// Background task keyword sets (aligned with Manager categories)
	titleKeywords := []string{
		"write a 5-10 word title", "Please write a 5-10 word title", "Respond with the title",
		"Generate a title for", "Create a brief title", "title for the conversation", "conversation title",
		"生成标题", "为对话起个标题",
	}
	summaryKeywords := []string{
		"Summarize this coding conversation", "Summarize the conversation", "Concise summary",
		"in under 50 characters", "compress the context", "Provide a concise summary",
		"condense the previous messages", "shorten the conversation history", "extract key points from",
	}
	suggestionKeywords := []string{
		"prompt suggestion generator", "suggest next prompts", "what should I ask next",
		"generate follow-up questions", "recommend next steps", "possible next actions",
	}
	systemKeywords := []string{
		"Warmup", "<system-reminder>", "This is a system message",
	}
	probeKeywords := []string{
		"check current directory", "list available tools", "verify environment", "test connection",
	}

	taskModel := ""
	switch {
	case matchesAnyKeyword(preview, systemKeywords):
		taskModel = "gemini-2.5-flash-lite"
	case matchesAnyKeyword(preview, titleKeywords):
		taskModel = "gemini-2.5-flash-lite"
	case matchesAnyKeyword(preview, summaryKeywords):
		// Simple summaries fall back to lite, context compression to standard flash
		if strings.Contains(preview, "in under 50 characters") {
			taskModel = "gemini-2.5-flash-lite"
		} else {
			taskModel = "gemini-2.5-flash"
		}
	case matchesAnyKeyword(preview, suggestionKeywords):
		taskModel = "gemini-2.5-flash-lite"
	case matchesAnyKeyword(preview, probeKeywords):
		taskModel = "gemini-2.5-flash-lite"
	}

	if taskModel == "" {
		return false, "", body
	}

	// Strip tools and thinking config
	delete(req, "tools")
	delete(req, "thinking")

	// Remove thinking/redacted_thinking blocks from message contents
	for i, m := range messages {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		var filtered []interface{}
		for _, b := range blocks {
			if bm, ok := b.(map[string]interface{}); ok {
				if t, _ := bm["type"].(string); t == "thinking" || t == "redacted_thinking" {
					continue
				}
			}
			filtered = append(filtered, b)
		}
		msg["content"] = filtered
		messages[i] = msg
	}
	req["messages"] = messages

	newBody, err := json.Marshal(req)
	if err != nil {
		return true, taskModel, body
	}
	return true, taskModel, newBody
}

// injectGoogleSearchTool injects googleSearch tool if not already present
// and no functionDeclarations exist (can't mix search with functions)
func injectGoogleSearchTool(innerRequest map[string]interface{}) {
	tools, ok := innerRequest["tools"].([]interface{})
	if !ok {
		tools = []interface{}{}
	}

	// Check if functionDeclarations already exist
	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			if _, hasFuncDecls := toolMap["functionDeclarations"]; hasFuncDecls {
				// Can't mix search tools with function declarations
				return
			}
		}
	}

	// Remove existing googleSearch/googleSearchRetrieval
	var filteredTools []interface{}
	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			if _, ok := toolMap["googleSearch"]; ok {
				continue
			}
			if _, ok := toolMap["googleSearchRetrieval"]; ok {
				continue
			}
		}
		filteredTools = append(filteredTools, tool)
	}

	// Add googleSearch
	filteredTools = append(filteredTools, map[string]interface{}{
		"googleSearch": map[string]interface{}{},
	})

	innerRequest["tools"] = filteredTools
}
