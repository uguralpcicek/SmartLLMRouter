package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"smartllmrouter/config"
)

// open ai request
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// pass anything else through as-is
	Extra map[string]json.RawMessage `json:"-"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// convert to gemini request
type GeminiRequest struct {
	Contents []GeminiContent `json:"contents"`
}

type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

// what gemini sends back
type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// open ai response
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Router struct {
	cfg    *config.Config
	client *http.Client
}

func New(cfg *config.Config) *Router {
	return &Router{
		cfg:    cfg,
		client: &http.Client{},
	}
}

func (r *Router) Handle(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", 500)
		return
	}

	var chatReq ChatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		http.Error(w, "bad json", 400)
		return
	}

	// grab the last user message to measure prompt length
	prompt := extractLastUserPrompt(chatReq.Messages)

	if len(prompt) < r.cfg.PromptThreshold {
		log.Printf("[router] short prompt (%d chars) -> cheap model", len(prompt))
		r.routeToGemini(w, chatReq)
	} else {
		log.Printf("[router] long prompt (%d chars) -> gpt-4o", len(prompt))
		r.routeToOpenAI(w, body)
	}
}

func extractLastUserPrompt(msgs []ChatMessage) string {
	// walk backwards to find the last user turn
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	// fallback: concat everything
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Content)
	}
	return sb.String()
}

// routeToOpenAI just proxies the raw body straight to OpenAI, easy
func (r *Router) routeToOpenAI(w http.ResponseWriter, rawBody []byte) {
	// swap the model to our configured expensive one
	var payload map[string]json.RawMessage
	json.Unmarshal(rawBody, &payload)
	modelJSON, _ := json.Marshal(r.cfg.ExpensiveModel)
	payload["model"] = modelJSON
	newBody, _ := json.Marshal(payload)

	oaiReq, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(newBody))
	oaiReq.Header.Set("Authorization", "Bearer "+r.cfg.OpenAIKey)
	oaiReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(oaiReq)
	if err != nil {
		log.Printf("openai request failed: %v", err)
		http.Error(w, "upstream error", 500)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// routeToGemini converts the request, calls gemini and wraps the response in openai format
func (r *Router) routeToGemini(w http.ResponseWriter, chatReq ChatRequest) {
	gemReq := toGeminiRequest(chatReq.Messages)
	payload, err := json.Marshal(gemReq)
	if err != nil {
		http.Error(w, "marshal error", 500)
		return
	}

	// model is in the url for gemini
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		r.cfg.CheapModel, r.cfg.GeminiKey,
	)

	httpReq, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		log.Printf("gemini request failed: %v", err)
		http.Error(w, "upstream error", 500)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("gemini returned %d: %s", resp.StatusCode, string(respBody))
		http.Error(w, "upstream error", 502)
		return
	}

	var gemResp GeminiResponse
	if err := json.Unmarshal(respBody, &gemResp); err != nil {
		log.Printf("failed to parse gemini response: %v", err)
		http.Error(w, "parse error", 500)
		return
	}

	oaiResp := wrapGeminiResponse(gemResp, r.cfg.CheapModel)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(oaiResp)
}

func toGeminiRequest(msgs []ChatMessage) GeminiRequest {
	var contents []GeminiContent
	for _, m := range msgs {
		role := m.Role
		// gemini uses "model" instead of "assistant"
		if role == "assistant" {
			role = "model"
		}
		// gemini doesn't support system role natively, just treat it as user
		if role == "system" {
			role = "user"
		}
		contents = append(contents, GeminiContent{
			Role:  role,
			Parts: []GeminiPart{{Text: m.Content}},
		})
	}
	return GeminiRequest{Contents: contents}
}

func wrapGeminiResponse(gr GeminiResponse, model string) OpenAIResponse {
	text := ""
	if len(gr.Candidates) > 0 && len(gr.Candidates[0].Content.Parts) > 0 {
		text = gr.Candidates[0].Content.Parts[0].Text
	}

	// TODO: implement real token counting later, using char count for now
	promptTok := gr.UsageMetadata.PromptTokenCount
	completionTok := gr.UsageMetadata.CandidatesTokenCount

	return OpenAIResponse{
		ID:      "chatcmpl-gemini-proxied",
		Object:  "chat.completion",
		Created: 0, // don't care about timestamp for now
		Model:   model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      ChatMessage{Role: "assistant", Content: text},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     promptTok,
			CompletionTokens: completionTok,
			TotalTokens:      promptTok + completionTok,
		},
	}
}
