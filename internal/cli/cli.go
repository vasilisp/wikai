package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/vasilisp/wikai/internal/api"
	"github.com/vasilisp/wikai/internal/util"
	"github.com/vasilisp/wikai/pkg/backai"
)

func askGPT(args []string, port int) {
	var query string

	if len(args) == 0 {
		// Read from stdin
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatal("Failed to read from stdin:", err)
		}
		query = string(input)
	} else {
		query = strings.Join(args, " ")
	}

	// Create HTTP client
	client := &http.Client{}

	// Create request
	req, err := http.NewRequest("POST", fmt.Sprintf("http://localhost:%d%s", port, api.PostPath), strings.NewReader(query))
	if err != nil {
		log.Fatal("Failed to create request:", err)
	}

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Failed to send request:", err)
	}
	defer resp.Body.Close()

	// Read response
	var result backai.Response

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Failed to get response: %s", resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal("Failed to decode response:", err)
	}

	fmt.Println(result)
}

func Main(args []string) {
	askGPT(args, 8080)
}

func Index(args []string) {
	if len(args) == 0 {
		log.Fatal("Usage: wikai index <ids>")
	}

	client := &http.Client{}

	var builder strings.Builder
	for i, arg := range args {
		pagePath := strings.TrimSuffix(arg, ".md")

		if err := util.ValidatePagePath(pagePath); err != nil {
			log.Fatal("invalid page path: %w", err)
		}

		builder.WriteString(pagePath)

		if i < len(args)-1 {
			builder.WriteString("\n")
		}
	}
	paths := builder.String()

	req, err := http.NewRequest("POST", fmt.Sprintf("http://localhost:%d%s", 8080, api.IndexPath), strings.NewReader(paths))
	if err != nil {
		log.Fatal("Failed to create request:", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Failed to send request:", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Failed to index: %s", resp.Status)
	}

	log.Printf("Indexed %d pages", len(args))
}
