package converter

import (
	"encoding/json"
	"strings"
)

// SSEEvent represents a parsed SSE event
type SSEEvent struct {
	Event string          `json:"event,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ParseSSE parses SSE text into events, returning parsed events and remaining buffer
func ParseSSE(text string) ([]SSEEvent, string) {
	var events []SSEEvent
	lines := strings.Split(text, "\n")

	var currentEvent string
	var currentData []string

	for i, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Empty line = end of event
		if trimmedLine == "" {
			// If we have accumulated data, try to parse it
			if len(currentData) > 0 {
				dataStr := strings.Join(currentData, "")
				if dataStr == "[DONE]" {
					events = append(events, SSEEvent{Event: "done"})
				} else {
					var rawData json.RawMessage
					if err := json.Unmarshal([]byte(dataStr), &rawData); err == nil {
						events = append(events, SSEEvent{
							Event: currentEvent,
							Data:  rawData,
						})
					} else {
						// JSON parsing failed - log and skip
						// This might be incomplete JSON, will be retried with next chunk
					}
				}
				currentEvent = ""
				currentData = nil
			}
			continue
		}

		// Process non-empty lines
		if strings.HasPrefix(trimmedLine, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "event:"))
		} else if strings.HasPrefix(trimmedLine, "data:") {
			dataValue := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "data:"))
			currentData = append(currentData, dataValue)
		}

		// Check if this is the last line and might be incomplete
		// A line is incomplete if it's the last line, not empty, and text doesn't end with \n
		if i == len(lines)-1 && line != "" && !strings.HasSuffix(text, "\n") {
			// Return the incomplete line as remaining buffer
			return events, text[strings.LastIndex(text, line):]
		}
	}

	return events, ""
}

// IsSSE checks if text looks like SSE format
func IsSSE(text string) bool {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "data:") {
			return true
		}
		// If we find a non-SSE line first, it's not SSE
		if line != "" {
			return false
		}
	}
	return false
}

// FormatSSE formats an event and data as SSE
func FormatSSE(event string, data interface{}) []byte {
	var sb strings.Builder
	if event != "" {
		sb.WriteString("event: ")
		sb.WriteString(event)
		sb.WriteString("\n")
	}

	var dataBytes []byte
	switch v := data.(type) {
	case []byte:
		dataBytes = v
	case string:
		dataBytes = []byte(v)
	default:
		dataBytes, _ = json.Marshal(v)
	}

	sb.WriteString("data: ")
	sb.Write(dataBytes)
	sb.WriteString("\n\n")

	return []byte(sb.String())
}

// FormatDone returns the SSE [DONE] marker
func FormatDone() []byte {
	return []byte("data: [DONE]\n\n")
}
