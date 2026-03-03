package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"
)

type claudeSSEEvent struct {
	eventType string
	data      map[string]interface{}
}

func parseSSELine(line string) (string, string, bool) {
	if idx := strings.IndexByte(line, ':'); idx >= 0 {
		key := line[:idx]
		value := strings.TrimLeft(line[idx+1:], " \t")
		return key, value, true
	}
	return "", "", false
}

// collectClaudeSSEToJSON collects a Claude SSE stream (event/data lines) into a single Claude JSON response.
// Mirrors Antigravity-Manager's `collect_stream_to_json` behavior.
func collectClaudeSSEToJSON(sse string) ([]byte, error) {
	events := make([]claudeSSEEvent, 0, 64)
	var currentEventType string
	var currentData string

	lines := strings.Split(sse, "\n")
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			if currentData != "" {
				var data map[string]interface{}
				if err := json.Unmarshal([]byte(currentData), &data); err == nil {
					events = append(events, claudeSSEEvent{
						eventType: currentEventType,
						data:      data,
					})
				}
				currentEventType = ""
				currentData = ""
			}
			continue
		}

		key, value, ok := parseSSELine(line)
		if !ok {
			continue
		}
		switch key {
		case "event":
			currentEventType = value
		case "data":
			currentData = value
		}
	}

	// Flush trailing event without terminating blank line (best-effort).
	if currentData != "" {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(currentData), &data); err == nil {
			events = append(events, claudeSSEEvent{
				eventType: currentEventType,
				data:      data,
			})
		}
	}

	response := map[string]interface{}{
		"id":          "msg_unknown",
		"type":        "message",
		"role":        "assistant",
		"model":       "",
		"content":     []interface{}{},
		"stop_reason": "end_turn",
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	content := make([]interface{}, 0, 16)

	var currentText strings.Builder
	var currentThinking strings.Builder
	var currentToolUse map[string]interface{}
	var currentToolInput strings.Builder

	for _, event := range events {
		switch event.eventType {
		case "message_start":
			if msg, ok := event.data["message"].(map[string]interface{}); ok {
				if id, ok := msg["id"].(string); ok && id != "" {
					response["id"] = id
				}
				if model, ok := msg["model"].(string); ok {
					response["model"] = model
				}
				if usage, ok := msg["usage"].(map[string]interface{}); ok {
					response["usage"] = usage
				}
			}

		case "content_block_start":
			cb, ok := event.data["content_block"].(map[string]interface{})
			if !ok {
				continue
			}
			cbType, _ := cb["type"].(string)
			switch cbType {
			case "text":
				currentText.Reset()
			case "thinking":
				currentThinking.Reset()
			case "tool_use":
				currentToolUse = cb
				currentToolInput.Reset()
			}

		case "content_block_delta":
			delta, ok := event.data["delta"].(map[string]interface{})
			if !ok {
				continue
			}
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				if t, ok := delta["text"].(string); ok {
					currentText.WriteString(t)
				}
			case "thinking_delta":
				if t, ok := delta["thinking"].(string); ok {
					currentThinking.WriteString(t)
				}
			case "input_json_delta":
				if p, ok := delta["partial_json"].(string); ok {
					currentToolInput.WriteString(p)
				}
			}

		case "content_block_stop":
			if currentText.Len() > 0 {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": currentText.String(),
				})
				currentText.Reset()
				continue
			}
			if currentThinking.Len() > 0 {
				content = append(content, map[string]interface{}{
					"type":     "thinking",
					"thinking": currentThinking.String(),
				})
				currentThinking.Reset()
				continue
			}
			if currentToolUse != nil {
				id, _ := currentToolUse["id"].(string)
				if id == "" {
					id = "unknown"
				}
				name, _ := currentToolUse["name"].(string)
				if name == "" {
					name = "unknown"
				}

				input := map[string]interface{}{}
				if currentToolInput.Len() > 0 {
					_ = json.Unmarshal([]byte(currentToolInput.String()), &input)
				}

				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    id,
					"name":  name,
					"input": input,
				})
				currentToolUse = nil
				currentToolInput.Reset()
				continue
			}

		case "message_delta":
			if delta, ok := event.data["delta"].(map[string]interface{}); ok {
				if stopReason, ok := delta["stop_reason"].(string); ok && stopReason != "" {
					response["stop_reason"] = stopReason
				}
			}
			if usage, ok := event.data["usage"].(map[string]interface{}); ok {
				response["usage"] = usage
			}

		case "message_stop":
			goto done

		case "error":
			return nil, fmt.Errorf("stream error: %v", event.data)
		}
	}

done:
	response["content"] = content
	return json.Marshal(response)
}

