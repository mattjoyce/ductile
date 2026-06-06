package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func runPluginNoun(args []string) int {
	if len(args) < 1 {
		printPluginNounHelp(os.Stderr)
		return 1
	}

	if isHelpToken(args[0]) {
		printPluginNounHelp(os.Stdout)
		return 0
	}

	action := args[0]
	actionArgs := args[1:]

	switch action {
	case "list":
		if hasHelpFlag(actionArgs) {
			printPluginListHelp()
			return 0
		}
		return runPluginList(actionArgs)
	case "run":
		if hasHelpFlag(actionArgs) {
			printPluginRunHelp()
			return 0
		}
		return runPluginRun(actionArgs)
	case "lock":
		if hasHelpFlag(actionArgs) {
			printPluginLockHelp()
			return 0
		}
		return runPluginLock(actionArgs)
	case "help":
		printPluginNounHelp(os.Stdout)
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown plugin action: %s\n", action)
		return 1
	}
}

func printPluginNounHelp(w *os.File) {
	_, _ = fmt.Fprintln(w, "Usage: ductile plugin <action>")
	_, _ = fmt.Fprintln(w, "Actions: list, run, lock")
}

func printPluginListHelp() {
	fmt.Println("Usage: ductile plugin list [--api-url URL] [--json]")
	fmt.Println("Show discovered plugins via the API /plugins endpoint.")
}

func printPluginRunHelp() {
	fmt.Println("Usage: ductile plugin run <name> [--command CMD] [--payload JSON] [--payload-file PATH] [--api-url URL] [--api-key KEY] [--json]")
	fmt.Println("Execute a plugin command via the API /plugin/{name}/{command} endpoint.")
}

type pluginListResponse struct {
	Plugins []struct {
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Commands    []string `json:"commands"`
	} `json:"plugins"`
}

type triggerRequest struct {
	Payload json.RawMessage `json:"payload,omitempty"`
}

type triggerResponse struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	Plugin  string `json:"plugin"`
	Command string `json:"command"`
}

func buildAPIURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func runPluginList(args []string) int {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	apiURL := fs.String("api-url", "http://localhost:8080", "Gateway API URL")
	jsonOut := fs.Bool("json", false, "Output JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if fs.NArg() > 0 {
		printPluginListHelp()
		return 1
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(buildAPIURL(*apiURL, "/plugins"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "API request failed: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read response: %v\n", err)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "API error (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}

	if *jsonOut {
		fmt.Println(string(body))
		return 0
	}

	var list pluginListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse response: %v\n", err)
		return 1
	}

	nameWidth := len("NAME")
	for _, p := range list.Plugins {
		if len(p.Name) > nameWidth {
			nameWidth = len(p.Name)
		}
	}

	fmt.Printf("%-*s  %-8s  %s\n", nameWidth, "NAME", "VERSION", "COMMANDS")
	for _, p := range list.Plugins {
		commands := strings.Join(p.Commands, ",")
		fmt.Printf("%-*s  %-8s  %s\n", nameWidth, p.Name, p.Version, commands)
		if strings.TrimSpace(p.Description) != "" {
			fmt.Printf("%*s  %s\n", nameWidth, "", p.Description)
		}
	}
	return 0
}

func runPluginRun(args []string) int {
	fs := flag.NewFlagSet("plugin run", flag.ContinueOnError)
	command := fs.String("command", "poll", "Plugin command to run")
	payloadRaw := fs.String("payload", "", "JSON payload to send")
	payloadFile := fs.String("payload-file", "", "Path to JSON payload file")
	apiURL := fs.String("api-url", "http://localhost:8080", "Gateway API URL")
	apiKey := fs.String("api-key", os.Getenv("DUCTILE_API_KEY"), "API Bearer Token")
	jsonOut := fs.Bool("json", false, "Output JSON response")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if fs.NArg() != 1 {
		printPluginRunHelp()
		return 1
	}
	if *apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: API key required. Use --api-key or DUCTILE_API_KEY env var.")
		return 1
	}
	if *payloadRaw != "" && *payloadFile != "" {
		fmt.Fprintln(os.Stderr, "Error: use only one of --payload or --payload-file")
		return 1
	}

	pluginName := fs.Arg(0)
	cmd := strings.TrimSpace(*command)
	if cmd == "" {
		fmt.Fprintln(os.Stderr, "Error: --command is required")
		return 1
	}

	var payload json.RawMessage
	if *payloadFile != "" {
		data, err := os.ReadFile(*payloadFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read payload file: %v\n", err)
			return 1
		}
		payload = json.RawMessage(bytes.TrimSpace(data))
	} else if *payloadRaw != "" {
		payload = json.RawMessage(strings.TrimSpace(*payloadRaw))
	}

	var body io.Reader
	if len(payload) > 0 {
		if !json.Valid(payload) {
			fmt.Fprintln(os.Stderr, "Error: payload must be valid JSON")
			return 1
		}
		var payloadObj any
		if err := json.Unmarshal(payload, &payloadObj); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid JSON payload: %v\n", err)
			return 1
		}
		if payloadObj != nil {
			if _, ok := payloadObj.(map[string]any); !ok {
				fmt.Fprintln(os.Stderr, "Error: payload must be a JSON object")
				return 1
			}
		}
		reqBody, err := json.Marshal(triggerRequest{Payload: payload})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build request: %v\n", err)
			return 1
		}
		body = bytes.NewBuffer(reqBody)
	}

	endpoint := fmt.Sprintf("/plugin/%s/%s", pluginName, cmd)
	req, err := http.NewRequest("POST", buildAPIURL(*apiURL, endpoint), body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+*apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "API request failed: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read response: %v\n", err)
		return 1
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "API error (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		return 1
	}

	if *jsonOut {
		fmt.Println(string(respBody))
		return 0
	}

	var result triggerResponse
	if err := json.Unmarshal(respBody, &result); err != nil || result.JobID == "" {
		fmt.Println(string(respBody))
		return 0
	}
	fmt.Printf("Queued job %s (%s %s)\n", result.JobID, result.Plugin, result.Command)
	return 0
}
