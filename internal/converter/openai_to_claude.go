package converter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Bowl42/maxx-next/internal/domain"
)

func init() {
	RegisterConverter(domain.ClientTypeOpenAI, domain.ClientTypeClaude, &openaiToClaudeRequest{}, &openaiToClaudeResponse{})
}

type openaiToClaudeRequest struct{}
type openaiToClaudeResponse struct{}

func (c *openaiToClaudeRequest) Transform(body []byte, model string, stream bool) ([]byte, error) {
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	claudeReq := ClaudeRequest{
		Model:       model,
		Stream:      stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	if req.MaxCompletionTokens > 0 && req.MaxTokens == 0 {
		claudeReq.MaxTokens = req.MaxCompletionTokens
	}

	// Convert messages
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			// Extract system message
			switch content := msg.Content.(type) {
			case string:
				claudeReq.System = content
			case []interface{}:
				var systemText string
				for _, part := range content {
					if m, ok := part.(map[string]interface{}); ok {
						if text, ok := m["text"].(string); ok {
							systemText += text
						}
					}
				}
				claudeReq.System = systemText
			}
			continue
		}

		claudeMsg := ClaudeMessage{Role: msg.Role}

		// Handle tool messages
		if msg.Role == "tool" {
			claudeMsg.Role = "user"
			contentStr, _ := msg.Content.(string)
			claudeMsg.Content = []ClaudeContentBlock{{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   contentStr,
			}}
			claudeReq.Messages = append(claudeReq.Messages, claudeMsg)
			continue
		}

		// Convert content
		switch content := msg.Content.(type) {
		case string:
			claudeMsg.Content = content
		case []interface{}:
			var blocks []ClaudeContentBlock
			for _, part := range content {
				if m, ok := part.(map[string]interface{}); ok {
					partType, _ := m["type"].(string)
					switch partType {
					case "text":
						text, _ := m["text"].(string)
						blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: text})
					case "image_url":
						if imgURL, ok := m["image_url"].(map[string]interface{}); ok {
							url, _ := imgURL["url"].(string)
							if strings.HasPrefix(url, "data:") {
								// Parse data URI: data:image/png;base64,xxxxx
								if semiIdx := strings.Index(url, ";base64,"); semiIdx != -1 {
									mediaType := url[5:semiIdx] // skip "data:"
									data := url[semiIdx+8:]     // skip ";base64,"
									blocks = append(blocks, ClaudeContentBlock{
										Type: "image",
										Source: &ClaudeImageSource{
											Type:      "base64",
											MediaType: mediaType,
											Data:      data,
										},
									})
								}
							} else if url != "" {
								// URL-based image - pass as url source type
								blocks = append(blocks, ClaudeContentBlock{
									Type: "image",
									Source: &ClaudeImageSource{
										Type: "url",
										URL:  url,
									},
								})
							}
						}
					}
				}
			}
			if len(blocks) == 1 && blocks[0].Type == "text" {
				claudeMsg.Content = blocks[0].Text
			} else {
				claudeMsg.Content = blocks
			}
		}

		// Handle tool calls
		if len(msg.ToolCalls) > 0 {
			var blocks []ClaudeContentBlock
			if text, ok := claudeMsg.Content.(string); ok && text != "" {
				blocks = append(blocks, ClaudeContentBlock{Type: "text", Text: text})
			}
			for _, tc := range msg.ToolCalls {
				var input interface{}
				if tc.Function.Arguments != "" {
					json.Unmarshal([]byte(tc.Function.Arguments), &input)
				}
				if input == nil {
					input = map[string]interface{}{}
				}
				blocks = append(blocks, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
			claudeMsg.Content = blocks
		}

		claudeReq.Messages = append(claudeReq.Messages, claudeMsg)
	}

	// Convert tools
	for _, tool := range req.Tools {
		claudeReq.Tools = append(claudeReq.Tools, ClaudeTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}

	// Convert tool_choice
	if req.ToolChoice != nil {
		switch tc := req.ToolChoice.(type) {
		case string:
			switch tc {
			case "required":
				claudeReq.ToolChoice = map[string]interface{}{"type": "any"}
			case "auto":
				claudeReq.ToolChoice = map[string]interface{}{"type": "auto"}
			case "none":
				// Claude doesn't have "none" - just don't set tool_choice
			}
		case map[string]interface{}:
			if tcType, _ := tc["type"].(string); tcType == "function" {
				if fn, ok := tc["function"].(map[string]interface{}); ok {
					if name, _ := fn["name"].(string); name != "" {
						claudeReq.ToolChoice = map[string]interface{}{
							"type": "tool",
							"name": name,
						}
					}
				}
			}
		}
	}

	// Convert stop
	switch stop := req.Stop.(type) {
	case string:
		claudeReq.StopSequences = []string{stop}
	case []interface{}:
		for _, s := range stop {
			if str, ok := s.(string); ok {
				claudeReq.StopSequences = append(claudeReq.StopSequences, str)
			}
		}
	}

	return json.Marshal(claudeReq)
}

func (c *openaiToClaudeResponse) Transform(body []byte) ([]byte, error) {
	var resp OpenAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	claudeResp := ClaudeResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
		Usage: ClaudeUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if choice.Message != nil {
			// Extract reasoning_content as thinking block (DeepSeek/GLM style)
			// GLM's reasoning_content is treated as regular text to avoid blocking
			if choice.Message.ReasoningContent != "" {
				// Append reasoning content as text (not as thinking block)
				claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
					Type: "text",
					Text: choice.Message.ReasoningContent,
				})
			}

			// Convert content
			if content, ok := choice.Message.Content.(string); ok && content != "" {
				// Extract <think> tags from content (open-source model convention)
				textContent := content
				if thinkContent, remaining, found := extractThinkTags(content); found {
					claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
						Type:      "thinking",
						Thinking:  thinkContent,
						Signature: fmt.Sprintf("%d", time.Now().UnixMilli()),
					})
					textContent = strings.TrimSpace(remaining)
				}
				if textContent != "" {
					claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
						Type: "text",
						Text: textContent,
					})
				}
			}

			// Convert tool calls
			for _, tc := range choice.Message.ToolCalls {
				var input interface{}
				if tc.Function.Arguments != "" {
					json.Unmarshal([]byte(tc.Function.Arguments), &input)
				}
				if input == nil {
					input = map[string]interface{}{}
				}
				claudeResp.Content = append(claudeResp.Content, ClaudeContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}

			// Map finish reason
			switch choice.FinishReason {
			case "stop":
				claudeResp.StopReason = "end_turn"
			case "length":
				claudeResp.StopReason = "max_tokens"
			case "tool_calls", "function_call":
				claudeResp.StopReason = "tool_use"
			}
		}
	}

	// Ensure at least one content block
	if len(claudeResp.Content) == 0 {
		claudeResp.Content = []ClaudeContentBlock{{Type: "text", Text: ""}}
	}

	return json.Marshal(claudeResp)
}

// extractThinkTags extracts content from <think>...</think> or <thinking>...</thinking> tags
func extractThinkTags(content string) (thinking string, remaining string, found bool) {
	for _, tag := range []string{"think", "thinking"} {
		openTag := "<" + tag + ">"
		closeTag := "</" + tag + ">"
		startIdx := strings.Index(content, openTag)
		if startIdx == -1 {
			continue
		}
		endIdx := strings.Index(content, closeTag)
		if endIdx == -1 {
			// Unclosed tag - treat everything after open tag as thinking
			thinking = strings.TrimSpace(content[startIdx+len(openTag):])
			remaining = strings.TrimSpace(content[:startIdx])
			return thinking, remaining, true
		}
		thinking = strings.TrimSpace(content[startIdx+len(openTag) : endIdx])
		remaining = strings.TrimSpace(content[:startIdx] + content[endIdx+len(closeTag):])
		return thinking, remaining, true
	}
	return "", content, false
}

// nextBlockIndex returns the next available content block index
func nextBlockIndex(state *TransformState) int {
	idx := 0
	if state.ThinkingSent {
		idx++
	}
	if state.ContentSent {
		idx++
	}
	// Add tool call block count
	for range state.ToolCalls {
		idx++
	}
	return idx
}

// textBlockIndex returns the index used for the text content block
func textBlockIndex(state *TransformState) int {
	if state.ThinkingSent {
		return 1
	}
	return 0
}

func (c *openaiToClaudeResponse) TransformChunk(chunk []byte, state *TransformState) ([]byte, error) {
	events, remaining := ParseSSE(state.Buffer + string(chunk))
	state.Buffer = remaining

	var output []byte
	for _, event := range events {
		if event.Event == "done" {
			// Send message_stop
			output = append(output, FormatSSE("message_stop", map[string]string{"type": "message_stop"})...)
			state.StopReason = "done" // Mark that we sent message_stop
			continue
		}

		var openaiChunk OpenAIStreamChunk
		if err := json.Unmarshal(event.Data, &openaiChunk); err != nil {
			continue
		}

		if len(openaiChunk.Choices) == 0 {
			// Some APIs send usage in a separate chunk with no choices
			if openaiChunk.Usage != nil {
				state.Usage.InputTokens = openaiChunk.Usage.PromptTokens
				state.Usage.OutputTokens = openaiChunk.Usage.CompletionTokens
			}
			continue
		}

		if openaiChunk.Usage != nil {
			state.Usage.InputTokens = openaiChunk.Usage.PromptTokens
			state.Usage.OutputTokens = openaiChunk.Usage.CompletionTokens
		}

		choice := openaiChunk.Choices[0]

		// First chunk - send message_start
		if state.MessageID == "" {
			state.MessageID = openaiChunk.ID

			msgStart := map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":    openaiChunk.ID,
					"type":  "message",
					"role":  "assistant",
					"model": openaiChunk.Model,
					"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
				},
			}
			output = append(output, FormatSSE("message_start", msgStart)...)
		}

		if choice.Delta != nil {
			// Reasoning content (GLM/DeepSeek thinking via reasoning_content field)
			// GLM sends ONLY reasoning_content initially, then content later
			// We must convert reasoning_content to regular text to avoid blocking
			if choice.Delta.ReasoningContent != "" {
				// Treat reasoning_content as regular text, not thinking block
				// This prevents the "stuck in thinking" issue
				if !state.ContentSent {
					// Start text block
					textIdx := textBlockIndex(state)
					blockStart := map[string]interface{}{
						"type":  "content_block_start",
						"index": textIdx,
						"content_block": map[string]interface{}{
							"type": "text",
							"text": "",
						},
					}
					output = append(output, FormatSSE("content_block_start", blockStart)...)
					state.ContentSent = true
				}

				// Send reasoning content as text delta
				textDelta := map[string]interface{}{
					"type":  "content_block_delta",
					"index": textBlockIndex(state),
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": choice.Delta.ReasoningContent,
					},
				}
				output = append(output, FormatSSE("content_block_delta", textDelta)...)
			}

			// Text content - also handle <think> tags from open-source models
			if content, ok := choice.Delta.Content.(string); ok && content != "" {
				content = handleStreamingThinkTags(content, state, &output)
				if content != "" {
					textIdx := textBlockIndex(state)

					if !state.ContentSent {
						blockStart := map[string]interface{}{
							"type":  "content_block_start",
							"index": textIdx,
							"content_block": map[string]interface{}{
								"type": "text",
								"text": "",
							},
						}
						output = append(output, FormatSSE("content_block_start", blockStart)...)
						state.ContentSent = true
					}

					delta := map[string]interface{}{
						"type":  "content_block_delta",
						"index": textIdx,
						"delta": map[string]interface{}{
							"type": "text_delta",
							"text": content,
						},
					}
					output = append(output, FormatSSE("content_block_delta", delta)...)
				}
			}

			// Tool calls streaming
			if len(choice.Delta.ToolCalls) > 0 {
				for _, tc := range choice.Delta.ToolCalls {
					tcIdx := tc.Index

					if _, exists := state.ToolCalls[tcIdx]; !exists {
						// New tool call - close thinking/text blocks first if needed
						if state.ThinkingSent && !state.ThinkingStopped {
							sigDelta := map[string]interface{}{
								"type":  "content_block_delta",
								"index": 0,
								"delta": map[string]interface{}{
									"type":      "signature_delta",
									"signature": fmt.Sprintf("%d", time.Now().UnixMilli()),
								},
							}
							output = append(output, FormatSSE("content_block_delta", sigDelta)...)
							output = append(output, FormatSSE("content_block_stop", map[string]interface{}{
								"type": "content_block_stop", "index": 0,
							})...)
							state.ThinkingStopped = true
						}
						if state.ContentSent && !state.ContentStopped {
							output = append(output, FormatSSE("content_block_stop", map[string]interface{}{
								"type": "content_block_stop", "index": textBlockIndex(state),
							})...)
							state.ContentStopped = true
						}

						// Determine block index for this tool_use
						blockIdx := nextBlockIndex(state)

						state.ToolCalls[tcIdx] = &ToolCallState{
							ID:        tc.ID,
							Name:      tc.Function.Name,
							BlockIndex: blockIdx,
						}

						// Send content_block_start for tool_use
						blockStart := map[string]interface{}{
							"type":  "content_block_start",
							"index": blockIdx,
							"content_block": map[string]interface{}{
								"type": "tool_use",
								"id":   tc.ID,
								"name": tc.Function.Name,
								"input": map[string]interface{}{},
							},
						}
						output = append(output, FormatSSE("content_block_start", blockStart)...)
					}

					// Accumulate arguments and send delta
					if tc.Function.Arguments != "" {
						tcState := state.ToolCalls[tcIdx]
						tcState.Arguments += tc.Function.Arguments

						delta := map[string]interface{}{
							"type":  "content_block_delta",
							"index": tcState.BlockIndex,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": tc.Function.Arguments,
							},
						}
						output = append(output, FormatSSE("content_block_delta", delta)...)
					}
				}
			}
		}

		// Finish reason
		if choice.FinishReason != "" {
			// Close thinking block if open - send signature_delta first
			if state.ThinkingSent && !state.ThinkingStopped {
				sigDelta := map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]interface{}{
						"type":      "signature_delta",
						"signature": fmt.Sprintf("%d", time.Now().UnixMilli()),
					},
				}
				output = append(output, FormatSSE("content_block_delta", sigDelta)...)
				output = append(output, FormatSSE("content_block_stop", map[string]interface{}{
					"type": "content_block_stop", "index": 0,
				})...)
				state.ThinkingStopped = true
			}

			// Close text block if open
			if state.ContentSent && !state.ContentStopped {
				output = append(output, FormatSSE("content_block_stop", map[string]interface{}{
					"type": "content_block_stop", "index": textBlockIndex(state),
				})...)
				state.ContentStopped = true
			}

			// Close all tool_use blocks
			for _, tc := range state.ToolCalls {
				output = append(output, FormatSSE("content_block_stop", map[string]interface{}{
					"type": "content_block_stop", "index": tc.BlockIndex,
				})...)
			}

			// Map finish reason
			stopReason := "end_turn"
			switch choice.FinishReason {
			case "length":
				stopReason = "max_tokens"
			case "tool_calls", "function_call":
				stopReason = "tool_use"
			}

			// Send message_delta with stop reason
			msgDelta := map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason": stopReason,
				},
				"usage": map[string]int{"output_tokens": state.Usage.OutputTokens},
			}
			output = append(output, FormatSSE("message_delta", msgDelta)...)
		}
	}

	return output, nil
}

// handleStreamingThinkTags detects and handles <think>/<thinking> tags in streaming content.
// Returns the remaining text content after stripping think tags.
func handleStreamingThinkTags(content string, state *TransformState, output *[]byte) string {
	// Already exited think mode - pass through
	if state.ThinkTagMode == 2 {
		return content
	}

	// Check if we're entering think mode
	if state.ThinkTagMode == 0 {
		// Check for opening tags
		for _, tag := range []string{"<think>", "<thinking>"} {
			idx := strings.Index(content, tag)
			if idx != -1 {
				state.ThinkTagMode = 1
				// Text before the tag is regular content
				before := content[:idx]
				after := content[idx+len(tag):]

				// Start thinking block
				if !state.ThinkingSent {
					blockStart := map[string]interface{}{
						"type":  "content_block_start",
						"index": 0,
						"content_block": map[string]interface{}{
							"type":     "thinking",
							"thinking": "",
						},
					}
					*output = append(*output, FormatSSE("content_block_start", blockStart)...)
					state.ThinkingSent = true
				}

				// Check if closing tag is in the same chunk
				remaining := handleStreamingThinkTags(after, state, output)
				if before != "" {
					return before + remaining
				}
				return remaining
			}
		}
		// No tag found - regular content
		return content
	}

	// Inside think mode (ThinkTagMode == 1) - look for closing tag
	for _, tag := range []string{"</think>", "</thinking>"} {
		idx := strings.Index(content, tag)
		if idx != -1 {
			// Found closing tag
			thinkContent := content[:idx]
			if thinkContent != "" {
				thinkingDelta := map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]interface{}{
						"type":     "thinking_delta",
						"thinking": thinkContent,
					},
				}
				*output = append(*output, FormatSSE("content_block_delta", thinkingDelta)...)
			}
			state.ThinkTagMode = 2 // Exited think mode
			remaining := strings.TrimSpace(content[idx+len(tag):])
			return remaining
		}
	}

	// Still inside think tags - emit as thinking delta
	if content != "" {
		thinkingDelta := map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type":     "thinking_delta",
				"thinking": content,
			},
		}
		*output = append(*output, FormatSSE("content_block_delta", thinkingDelta)...)
	}
	return "" // All content consumed as thinking
}
