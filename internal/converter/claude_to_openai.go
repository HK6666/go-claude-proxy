package converter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Bowl42/maxx-next/internal/domain"
)

func init() {
	RegisterConverter(domain.ClientTypeClaude, domain.ClientTypeOpenAI, &claudeToOpenAIRequest{}, &claudeToOpenAIResponse{})
}

type claudeToOpenAIRequest struct{}
type claudeToOpenAIResponse struct{}

func (c *claudeToOpenAIRequest) Transform(body []byte, model string, stream bool) ([]byte, error) {
	var req ClaudeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	openaiReq := OpenAIRequest{
		Model:       model,
		Stream:      stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	// Convert system to first message
	if req.System != nil {
		switch s := req.System.(type) {
		case string:
			openaiReq.Messages = append(openaiReq.Messages, OpenAIMessage{
				Role:    "system",
				Content: s,
			})
		case []interface{}:
			var systemText string
			for _, block := range s {
				if m, ok := block.(map[string]interface{}); ok {
					if text, ok := m["text"].(string); ok {
						systemText += text
					}
				}
			}
			if systemText != "" {
				openaiReq.Messages = append(openaiReq.Messages, OpenAIMessage{
					Role:    "system",
					Content: systemText,
				})
			}
		}
	}

	// Convert messages
	for _, msg := range req.Messages {
		openaiMsg := OpenAIMessage{Role: msg.Role}
		var toolResults []OpenAIMessage
		switch content := msg.Content.(type) {
		case string:
			openaiMsg.Content = content
		case []interface{}:
			var parts []OpenAIContentPart
			var toolCalls []OpenAIToolCall
			for _, block := range content {
				if m, ok := block.(map[string]interface{}); ok {
					blockType, _ := m["type"].(string)
					switch blockType {
					case "text":
						if text, ok := m["text"].(string); ok {
							parts = append(parts, OpenAIContentPart{Type: "text", Text: text})
						}
					case "image":
						if source, ok := m["source"].(map[string]interface{}); ok {
							mediaType, _ := source["media_type"].(string)
							data, _ := source["data"].(string)
							if mediaType != "" && data != "" {
								parts = append(parts, OpenAIContentPart{
									Type: "image_url",
									ImageURL: &OpenAIImageURL{
										URL: "data:" + mediaType + ";base64," + data,
									},
								})
							}
						}
					case "tool_use":
						id, _ := m["id"].(string)
						name, _ := m["name"].(string)
						input := m["input"]
						// Ensure arguments is a valid JSON object, never "null"
						args := "{}"
						if input != nil {
							if inputJSON, err := json.Marshal(input); err == nil && string(inputJSON) != "null" {
								args = string(inputJSON)
							}
						}
						toolCalls = append(toolCalls, OpenAIToolCall{
							Index: len(toolCalls), // Add index for GLM/ModelArts compatibility
							ID:   id,
							Type: "function",
							Function: OpenAIFunctionCall{Name: name, Arguments: args},
						})
					case "tool_result":
						toolUseID, _ := m["tool_use_id"].(string)
						// tool_result content can be a string or array of content blocks
						var resultContent string
						switch c := m["content"].(type) {
						case string:
							resultContent = c
						case []interface{}:
							for _, block := range c {
								if bm, ok := block.(map[string]interface{}); ok {
									if text, ok := bm["text"].(string); ok {
										resultContent += text
									}
								}
							}
						}
						toolResults = append(toolResults, OpenAIMessage{
							Role:       "tool",
							Content:    resultContent,
							ToolCallID: toolUseID,
						})
					}
				}
			}
			if len(toolCalls) > 0 {
				openaiMsg.ToolCalls = toolCalls
				// OpenAI requires content to be non-null on assistant messages with tool_calls
				// Some compatible APIs reject content: null
				if openaiMsg.Content == nil {
					openaiMsg.Content = ""
				}
			}
			if len(parts) == 1 && parts[0].Type == "text" {
				openaiMsg.Content = parts[0].Text
			} else if len(parts) > 0 {
				openaiMsg.Content = parts
			}
		}
		// Tool results must immediately follow the assistant message with tool_calls.
		// When a Claude user message contains both tool_result and text blocks,
		// emit tool results first, then the text as a separate user message.
		if len(toolResults) > 0 {
			for _, toolResult := range toolResults {
				openaiReq.Messages = append(openaiReq.Messages, toolResult)
			}
			// Add remaining text content as a separate user message after tool results
			if openaiMsg.Content != nil {
				openaiReq.Messages = append(openaiReq.Messages, openaiMsg)
			}
		} else if openaiMsg.Content != nil || len(openaiMsg.ToolCalls) > 0 {
			openaiReq.Messages = append(openaiReq.Messages, openaiMsg)
		}
	}

	// Convert tools
	for _, tool := range req.Tools {
		openaiReq.Tools = append(openaiReq.Tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	// Convert stop sequences
	if len(req.StopSequences) > 0 {
		openaiReq.Stop = req.StopSequences
	}

	// Convert tool_choice
	if req.ToolChoice != nil {
		switch tc := req.ToolChoice.(type) {
		case string:
			switch tc {
			case "any":
				openaiReq.ToolChoice = "required"
			case "auto":
				openaiReq.ToolChoice = "auto"
			case "none":
				openaiReq.ToolChoice = "none"
			}
		case map[string]interface{}:
			if tcType, _ := tc["type"].(string); tcType == "tool" {
				if name, _ := tc["name"].(string); name != "" {
					openaiReq.ToolChoice = map[string]interface{}{
						"type":     "function",
						"function": map[string]string{"name": name},
					}
				}
			}
		}
	}

	// Validate and fix message ordering:
	// OpenAI requires tool messages to immediately follow the assistant message with matching tool_calls.
	// Drop orphaned tool messages and ensure no invalid sequences.
	openaiReq.Messages = fixToolMessageOrdering(openaiReq.Messages)

	// GLM/ModelArts API does NOT support "tool" message role.
	// Convert tool-calling history into plain text so GLM understands the context:
	// - assistant with tool_calls → text description of what was called
	// - tool results → user message with the result
	openaiReq.Messages = flattenToolMessages(openaiReq.Messages)

	// Also strip tools definition if GLM doesn't support it
	// (keep it for now — only flatten the history messages)

	return json.Marshal(openaiReq)
}

// fixToolMessageOrdering validates and fixes message ordering for OpenAI compatibility.
// OpenAI requires: tool messages MUST immediately follow the assistant message with matching tool_calls,
// AND tool messages must be in the SAME ORDER as the tool_calls array.
// This function:
// 1. Drops empty assistant messages (e.g., thinking-only blocks that were stripped)
// 2. Collects tool_call IDs from each assistant message in order
// 3. Reorders tool messages to match tool_calls order
// 4. Drops orphaned tool messages with no matching tool_call
func fixToolMessageOrdering(messages []OpenAIMessage) []OpenAIMessage {
	if len(messages) == 0 {
		return messages
	}

	// First pass: drop empty assistant messages (thinking-only that were stripped)
	var cleaned []OpenAIMessage
	for _, msg := range messages {
		if msg.Role == "assistant" && isEmptyContent(msg.Content) && len(msg.ToolCalls) == 0 {
			continue
		}
		cleaned = append(cleaned, msg)
	}

	// Second pass: ensure tool messages are in the same order as tool_calls
	var result []OpenAIMessage
	var activeToolCallOrder []string  // Preserve order of tool_calls
	var activeToolCalls map[string]*OpenAIMessage  // Map tool_call_id -> tool message

	for _, msg := range cleaned {
		if msg.Role == "assistant" {
			// First, flush any pending tool messages in order
			for _, tcID := range activeToolCallOrder {
				if toolMsg, ok := activeToolCalls[tcID]; ok {
					result = append(result, *toolMsg)
					delete(activeToolCalls, tcID)
				}
			}
			// Clear state
			activeToolCallOrder = nil
			activeToolCalls = nil

			// Collect tool_call IDs in order
			if len(msg.ToolCalls) > 0 {
				activeToolCallOrder = make([]string, 0, len(msg.ToolCalls))
				activeToolCalls = make(map[string]*OpenAIMessage)
				for _, tc := range msg.ToolCalls {
					activeToolCallOrder = append(activeToolCallOrder, tc.ID)
				}
			}

			// Add the assistant message
			result = append(result, msg)

		} else if msg.Role == "tool" {
			// Collect tool messages to be reordered later
			if activeToolCalls != nil && msg.ToolCallID != "" {
				// Check if this tool_call_id is expected
				expected := false
				for _, tcID := range activeToolCallOrder {
					if tcID == msg.ToolCallID {
						expected = true
						break
					}
				}
				if expected {
					// Store the tool message (will be added in order later)
					msgCopy := msg
					activeToolCalls[msg.ToolCallID] = &msgCopy
				}
				// else: orphaned tool message, drop it
			} else {
				// No active tool_calls, just append
				result = append(result, msg)
			}

		} else {
			// user/system message - first flush pending tool messages, then add this message
			for _, tcID := range activeToolCallOrder {
				if toolMsg, ok := activeToolCalls[tcID]; ok {
					result = append(result, *toolMsg)
					delete(activeToolCalls, tcID)
				}
			}
			activeToolCallOrder = nil
			activeToolCalls = nil
			result = append(result, msg)
		}
	}

	// Flush any remaining tool messages
	for _, tcID := range activeToolCallOrder {
		if toolMsg, ok := activeToolCalls[tcID]; ok {
			result = append(result, *toolMsg)
		}
	}

	return result
}

// flattenToolMessages converts tool-calling messages into plain text for APIs
// that don't support the "tool" role (like GLM/ModelArts).
// - assistant messages with tool_calls → keeps text content, appends tool call descriptions
// - tool messages → converted to user messages with tool result text
// This preserves conversation context so the model understands what happened.
func flattenToolMessages(messages []OpenAIMessage) []OpenAIMessage {
	var result []OpenAIMessage

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Convert tool_calls to text description
			text, _ := msg.Content.(string)
			for _, tc := range msg.ToolCalls {
				text += fmt.Sprintf("\n[Called tool: %s(%s)]", tc.Function.Name, tc.Function.Arguments)
			}
			result = append(result, OpenAIMessage{
				Role:    "assistant",
				Content: strings.TrimSpace(text),
			})
		} else if msg.Role == "tool" {
			// Convert tool result to user message
			content, _ := msg.Content.(string)
			if content == "" {
				content = "(empty result)"
			}
			// Merge consecutive tool results into one user message
			toolText := fmt.Sprintf("[Tool result for %s]: %s", msg.ToolCallID, content)
			for i+1 < len(messages) && messages[i+1].Role == "tool" {
				i++
				nextContent, _ := messages[i].Content.(string)
				if nextContent == "" {
					nextContent = "(empty result)"
				}
				toolText += fmt.Sprintf("\n[Tool result for %s]: %s", messages[i].ToolCallID, nextContent)
			}
			result = append(result, OpenAIMessage{
				Role:    "user",
				Content: toolText,
			})
		} else {
			result = append(result, msg)
		}
	}

	// Fix consecutive same-role messages by merging them
	var merged []OpenAIMessage
	for _, msg := range result {
		if len(merged) > 0 && merged[len(merged)-1].Role == msg.Role {
			// Merge with previous message
			prev := &merged[len(merged)-1]
			prevText, _ := prev.Content.(string)
			currText, _ := msg.Content.(string)
			prev.Content = prevText + "\n" + currText
		} else {
			merged = append(merged, msg)
		}
	}

	return merged
}

// isEmptyContent checks if message content is effectively empty
func isEmptyContent(content interface{}) bool {
	if content == nil {
		return true
	}
	if s, ok := content.(string); ok && s == "" {
		return true
	}
	return false
}

func (c *claudeToOpenAIResponse) Transform(body []byte) ([]byte, error) {
	var resp ClaudeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	openaiResp := OpenAIResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Usage: OpenAIUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	// Convert content to message
	msg := OpenAIMessage{Role: "assistant"}
	var textContent string
	var toolCalls []OpenAIToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			textContent += block.Text
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, OpenAIToolCall{
				Index: len(toolCalls), // Add index for GLM/ModelArts compatibility
				ID:   block.ID,
				Type: "function",
				Function: OpenAIFunctionCall{Name: block.Name, Arguments: string(inputJSON)},
			})
		}
	}

	if textContent != "" {
		msg.Content = textContent
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	// Map stop reason
	finishReason := "stop"
	switch resp.StopReason {
	case "end_turn":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	}

	openaiResp.Choices = []OpenAIChoice{{
		Index:        0,
		Message:      &msg,
		FinishReason: finishReason,
	}}

	return json.Marshal(openaiResp)
}

func (c *claudeToOpenAIResponse) TransformChunk(chunk []byte, state *TransformState) ([]byte, error) {
	events, remaining := ParseSSE(state.Buffer + string(chunk))
	state.Buffer = remaining

	var output []byte
	for _, event := range events {
		if event.Event == "done" {
			output = append(output, FormatDone()...)
			continue
		}

		var claudeEvent ClaudeStreamEvent
		if err := json.Unmarshal(event.Data, &claudeEvent); err != nil {
			continue
		}

		switch claudeEvent.Type {
		case "message_start":
			if claudeEvent.Message != nil {
				state.MessageID = claudeEvent.Message.ID
			}
			chunk := OpenAIStreamChunk{
				ID:      state.MessageID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Choices: []OpenAIChoice{{
					Index: 0,
					Delta: &OpenAIMessage{Role: "assistant", Content: ""},
				}},
			}
			output = append(output, FormatSSE("", chunk)...)

		case "content_block_start":
			if claudeEvent.ContentBlock != nil {
				state.CurrentBlockType = claudeEvent.ContentBlock.Type
				state.CurrentIndex = claudeEvent.Index
				if claudeEvent.ContentBlock.Type == "tool_use" {
					state.ToolCalls[claudeEvent.Index] = &ToolCallState{
						ID:   claudeEvent.ContentBlock.ID,
						Name: claudeEvent.ContentBlock.Name,
					}
				}
			}

		case "content_block_delta":
			if claudeEvent.Delta != nil {
				switch claudeEvent.Delta.Type {
				case "text_delta":
					chunk := OpenAIStreamChunk{
						ID:      state.MessageID,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Choices: []OpenAIChoice{{
							Index: 0,
							Delta: &OpenAIMessage{Content: claudeEvent.Delta.Text},
						}},
					}
					output = append(output, FormatSSE("", chunk)...)
				case "input_json_delta":
					if tc, ok := state.ToolCalls[state.CurrentIndex]; ok {
						tc.Arguments += claudeEvent.Delta.PartialJSON
						chunk := OpenAIStreamChunk{
							ID:      state.MessageID,
							Object:  "chat.completion.chunk",
							Created: time.Now().Unix(),
							Choices: []OpenAIChoice{{
								Index: 0,
								Delta: &OpenAIMessage{
									ToolCalls: []OpenAIToolCall{{
										Index:    state.CurrentIndex,
										ID:       tc.ID,
										Type:     "function",
										Function: OpenAIFunctionCall{Name: tc.Name, Arguments: claudeEvent.Delta.PartialJSON},
									}},
								},
							}},
						}
						output = append(output, FormatSSE("", chunk)...)
					}
				}
			}

		case "message_delta":
			if claudeEvent.Delta != nil {
				state.StopReason = claudeEvent.Delta.StopReason
			}
			if claudeEvent.Usage != nil {
				state.Usage.OutputTokens = claudeEvent.Usage.OutputTokens
			}

		case "message_stop":
			finishReason := "stop"
			switch state.StopReason {
			case "end_turn":
				finishReason = "stop"
			case "max_tokens":
				finishReason = "length"
			case "tool_use":
				finishReason = "tool_calls"
			}
			chunk := OpenAIStreamChunk{
				ID:      state.MessageID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Choices: []OpenAIChoice{{
					Index:        0,
					Delta:        &OpenAIMessage{},
					FinishReason: finishReason,
				}},
			}
			output = append(output, FormatSSE("", chunk)...)
			output = append(output, FormatDone()...)
		}
	}

	return output, nil
}

// Add Index field to OpenAIToolCall for streaming
type OpenAIToolCallWithIndex struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function OpenAIFunctionCall `json:"function,omitempty"`
}
