package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Bowl42/maxx-next/internal/converter"
	"github.com/Bowl42/maxx-next/internal/domain"
)

var (
	glmAPIKey  string
	glmBaseURL string
	proxyPort  string
	registry   *converter.Registry
)

func init() {
	// Try loading .env file
	if data, err := os.ReadFile(".env"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				os.Setenv(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
	}

	glmAPIKey = os.Getenv("GLM_API_KEY")
	glmBaseURL = os.Getenv("GLM_BASE_URL")
	proxyPort = os.Getenv("PROXY_PORT")

	if glmAPIKey == "" {
		glmAPIKey = "sk-ZPBCpICn2lVtiFzevQ7G0OQiHgIx3wqD"
	}
	if glmBaseURL == "" {
		glmBaseURL = "https://voyage.prod.telepub.cn/voyage/api"
	}
	if proxyPort == "" {
		proxyPort = "27659"
	}

	// Trim trailing slash
	glmBaseURL = strings.TrimRight(glmBaseURL, "/")
}

func main() {
	registry = converter.NewRegistry()

	http.HandleFunc("/", handleHealth)
	http.HandleFunc("/v1/messages", handleMessages)

	addr := ":" + proxyPort
	log.Printf("Claude-to-OpenAI proxy listening on %s", addr)
	log.Printf("GLM endpoint: %s/v1/chat/completions", glmBaseURL)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok"}`)
}

// claudeMinimal is used only to peek at model and stream fields from the Claude request.
type claudeMinimal struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Peek at model and stream
	var peek claudeMinimal
	json.Unmarshal(body, &peek)

	log.Printf(">> Claude request: model=%s stream=%v (%d bytes)", peek.Model, peek.Stream, len(body))

	  // Map all Claude model names to GLM upstream model
	  upstreamModel := "glm-5"
	  log.Printf(">> Claude request: model=%s -> %s stream=%v (%d bytes)", peek.Model, upstreamModel, peek.Stream,
	  len(body))
	
	  // Convert Claude request → OpenAI request
	  openaiBody, err := registry.TransformRequest(
	      domain.ClientTypeClaude, domain.ClientTypeOpenAI,
	      body, upstreamModel, peek.Stream,
	  )
	if err != nil {
		log.Printf("request transform error: %v", err)
		writeClaudeError(w, http.StatusBadRequest, "request_transform_error", err.Error())
		return
	}

	// Disable GLM extended thinking (reasoning_content) to improve performance
	// GLM's reasoning_content can be very large and slow down responses significantly
	openaiBody, err = disableGLMThinking(openaiBody)
	if err != nil {
		log.Printf("failed to disable GLM thinking: %v", err)
		// Continue anyway - this is not critical
	}

	// Build upstream request
	upstreamURL := glmBaseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(openaiBody))
	if err != nil {
		writeClaudeError(w, http.StatusInternalServerError, "proxy_error", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+glmAPIKey)

	// Forward request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("upstream error: %v", err)
		writeClaudeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	if peek.Stream {
		handleStream(w, resp, openaiBody)
	} else {
		handleNonStream(w, resp, openaiBody)
	}
}

func handleNonStream(w http.ResponseWriter, resp *http.Response, openaiBody []byte) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeClaudeError(w, http.StatusBadGateway, "upstream_read_error", err.Error())
		return
	}

	log.Printf("<< OpenAI response: %d (%d bytes)", resp.StatusCode, len(respBody))

	if resp.StatusCode != http.StatusOK {
		log.Printf("<< OpenAI error body: %s", string(respBody))
		if resp.StatusCode == 400 {
			log.Printf(">> Request body that caused 400:\n%s", string(openaiBody))
		}
		writeClaudeError(w, resp.StatusCode, "upstream_error", string(respBody))
		return
	}

	// Convert OpenAI response → Claude response
	claudeBody, err := registry.TransformResponse(
		domain.ClientTypeOpenAI, domain.ClientTypeClaude,
		respBody,
	)
	if err != nil {
		log.Printf("response transform error: %v", err)
		writeClaudeError(w, http.StatusInternalServerError, "response_transform_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(claudeBody)
}

func handleStream(w http.ResponseWriter, resp *http.Response, openaiBody []byte) {
	if resp.StatusCode != http.StatusOK {
		// Non-200 in stream mode — read full body and return error
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("<< OpenAI stream error: %d: %s", resp.StatusCode, string(respBody))
		if resp.StatusCode == 400 {
			log.Printf(">> Request body that caused 400:\n%s", string(openaiBody))
		}
		writeClaudeError(w, resp.StatusCode, "upstream_error", string(respBody))
		return
	}

	// Log response headers for debugging
	log.Printf("<< Stream response headers: content-type=%s", resp.Header.Get("Content-Type"))

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeClaudeError(w, http.StatusInternalServerError, "proxy_error", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	state := converter.NewTransformState()
	buf := make([]byte, 32*1024)
	chunkCount := 0
	totalBytesRead := 0
	totalBytesWritten := 0

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			totalBytesRead += n
			chunkCount++
			// Log every chunk to help debug
			log.Printf("<< Stream chunk #%d, size=%d bytes", chunkCount, n)

			// Pass raw SSE bytes directly to TransformChunk —
			// it handles SSE parsing internally via ParseSSE + state.Buffer
			claudeChunk, transformErr := registry.TransformStreamChunk(
				domain.ClientTypeOpenAI, domain.ClientTypeClaude,
				buf[:n], state,
			)
			if transformErr != nil {
				log.Printf("stream transform error: %v", transformErr)
				continue
			}
			if len(claudeChunk) > 0 {
				totalBytesWritten += len(claudeChunk)
				w.Write(claudeChunk)
				flusher.Flush()
			} else {
				log.Printf("<< TransformStreamChunk returned empty data for chunk #%d", chunkCount)
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("stream read error: %v", err)
			} else {
				log.Printf("<< Stream EOF: total chunks=%d, read=%d, written=%d", chunkCount, totalBytesRead, totalBytesWritten)
			}
			break
		}
	}
}

func writeClaudeError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
}

func addThinkingDisabled(body []byte) ([]byte, error) {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	req["thinking"] = map[string]string{"type": "disabled"}
	return json.Marshal(req)
}

func disableGLMThinking(body []byte) ([]byte, error) {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	// Try to disable GLM's extended thinking (reasoning_content)
	// Common parameter names for different APIs
	req["include_reasoning"] = false
	req["extended_thinking"] = false
	// Also try to set max reasoning tokens to 0
	if req["max_tokens"] == nil {
		req["max_tokens"] = 4096
	}
	return json.Marshal(req)
}
