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
	Port                int    `json:"port,omitempty"`
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

	if config.WikiPrefix == "" {
		config.WikiPrefix = "/wikai"
	} else {
		if len(config.WikiPrefix) < 2 || config.WikiPrefix[0] != '/' {
			log.Fatal("WikiPrefix must start with /")
		}
		for i := 1; i < len(config.WikiPrefix); i++ {
			c := config.WikiPrefix[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
				log.Fatal("WikiPrefix must contain only alphanumeric characters after the /")
			}
		}
	}

	if config.Port <= 0 {
		config.Port = 8080
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
