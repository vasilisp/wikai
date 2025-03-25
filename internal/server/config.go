package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type config struct {
	WikiPath            string `json:"wikiPath"`
	WikiPrefix          string `json:"wikiPrefix,omitempty"`
	OpenAIToken         string `json:"openaiToken"`
	EmbeddingDimensions int    `json:"embeddingDimensions,omitempty"`
}

func loadConfig() *config {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatal("Failed to get home directory:", err)
	}

	configPath := filepath.Join(homeDir, ".config", "wikai.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatal("Failed to read config file:", err)
	}

	var config config
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatal("Failed to parse config file:", err)
	}

	// Set default wiki prefix if not specified
	if config.WikiPrefix == "" {
		config.WikiPrefix = "/wikai"
	}

	return &config
}

func wikiPath(config *config) (string, error) {
	wikiPath := config.WikiPath
	if wikiPath[:2] == "~/" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("Failed to get home directory: %v", err)
		}
		wikiPath = filepath.Join(homeDir, wikiPath[2:])
	}
	return wikiPath, nil
}
