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
	var result api.PostResponse

	if resp.StatusCode != http.StatusOK {
		log.Printf("Failed to get response: %s", resp.Status)
		os.Exit(1)
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal("Failed to decode response:", err)
	}

	fmt.Println(result)
}

func Main(args []string) {
	askGPT(args, 8080)
}
