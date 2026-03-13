package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	OpenAIKey   string `json:"openai_key"`
	GeminiKey   string `json:"gemini_key"`
	// threshold in chars, below this we use the cheap model
	PromptThreshold int    `json:"prompt_threshold"`
	CheapModel      string `json:"cheap_model"`
	ExpensiveModel  string `json:"expensive_model"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{}
	if err := json.NewDecoder(f).Decode(cfg); err != nil {
		return nil, err
	}

	// sensible defaults
	if cfg.PromptThreshold == 0 {
		cfg.PromptThreshold = 800
	}
	if cfg.CheapModel == "" {
		cfg.CheapModel = "gemini-1.5-flash"
	}
	if cfg.ExpensiveModel == "" {
		cfg.ExpensiveModel = "gpt-4o"
	}

	return cfg, nil
}
