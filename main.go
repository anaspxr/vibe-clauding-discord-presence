package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/tsanva/cc-discord-presence/discord"
)

const (
	// Discord Application ID for "Vibe Clauding"
	ClientID = "1561799669942583428"

	// Polling interval as fallback
	PollInterval = 3 * time.Second

	// Exit when no session data has been written for this long
	IdleExit = 30 * time.Minute
)

// Model pricing per million tokens (December 2025)
// Update these when new models are released: https://www.anthropic.com/pricing
var modelPricing = map[string]struct{ Input, Output float64 }{
	"claude-opus-4-5-20251101":   {15.0, 75.0},
	"claude-sonnet-4-5-20241022": {3.0, 15.0},
	"claude-sonnet-4-20250514":   {3.0, 15.0},
	"claude-haiku-4-5-20241022":  {1.0, 5.0},
}

// Model display names - add new model IDs here when released
var modelDisplayNames = map[string]string{
	"claude-opus-4-5-20251101":   "Opus 4.5",
	"claude-sonnet-4-5-20241022": "Sonnet 4.5",
	"claude-sonnet-4-20250514":   "Sonnet 4",
	"claude-haiku-4-5-20241022":  "Haiku 4.5",
}

// StatusLineData matches Claude Code's statusline JSON structure. The hooks
// module (hooks/register.ts) writes the same shape to its own file.
type StatusLineData struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	Cost struct {
		TotalCostUSD       float64 `json:"total_cost_usd"`
		TotalDurationMS    int64   `json:"total_duration_ms"`
		TotalAPIDurationMS int64   `json:"total_api_duration_ms"`
	} `json:"cost"`
	ContextWindow struct {
		TotalInputTokens  int64   `json:"total_input_tokens"`
		TotalOutputTokens int64   `json:"total_output_tokens"`
		ContextWindowSize int64   `json:"context_window_size"`
		UsedPercentage    float64 `json:"used_percentage"`
	} `json:"context_window"`
}

// SessionData holds parsed session information
type SessionData struct {
	ProjectName string
	ProjectPath string
	GitBranch   string
	ModelName   string
	TotalTokens int64
	WindowSize  int64   // 0 when the source has none (JSONL)
	UsedPercent float64 // of WindowSize
	TotalCost   float64
	StartTime   time.Time
}

// JSONLMessage represents a message entry in JSONL files
type JSONLMessage struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	claudeDir        string
	projectsDir      string
	dataFilePath     string
	moduleFilePath   string
	configFilePath   string
	pidFilePath      string
	refcountFilePath string
	sessionStartTime = time.Now()
	discordClient    *discord.Client
	currentSource    string
	nudgeShown       bool
	lastPresence     string
)

// Where session data came from, in the order readSessionData tries them.
const (
	sourceModule     = "hooks module"
	sourceStatusLine = "statusline data"
	sourceJSONL      = "JSONL fallback"
)

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting home directory: %v\n", err)
		os.Exit(1)
	}
	claudeDir = filepath.Join(home, ".claude")
	projectsDir = filepath.Join(claudeDir, "projects")
	dataFilePath = filepath.Join(claudeDir, "discord-presence-data.json")
	moduleFilePath = filepath.Join(claudeDir, "discord-presence-module.json")
	configFilePath = filepath.Join(claudeDir, "discord-presence-config.json")
	pidFilePath = filepath.Join(claudeDir, "discord-presence.pid")
	refcountFilePath = filepath.Join(claudeDir, "discord-presence.refcount")
}

func main() {
	fmt.Println(`
╔═══════════════════════════════════════════════════════════╗
║     Vibe Clauding - Discord Rich Presence                 ║
║     Show your Claude Code session on Discord!             ║
╚═══════════════════════════════════════════════════════════╝`)

	// Connect to Discord
	fmt.Println("🔗 Connecting to Discord...")
	discordClient = discord.NewClient(ClientID)
	if err := discordClient.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Discord: %v\n", err)
		fmt.Fprintln(os.Stderr, "   Make sure Discord is running and try again.")
		os.Exit(1)
	}
	fmt.Println("✓ Discord RPC connected!")

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n⏹ Shutting down...")
		discordClient.Close()
		os.Exit(0)
	}()

	go exitWhenIdle()

	// Try initial read and show data source
	if session := readSessionData(); session != nil {
		updatePresence(session)
		fmt.Printf("✓ Found active session: %s (using %s)\n", session.ProjectName, currentSource)
	} else {
		fmt.Println("⏳ Waiting for Claude Code session...")
	}

	fmt.Println("🎮 Discord Rich Presence is now active! Press Ctrl+C to stop.")

	// Start watching for changes
	watchForChanges()
}

// exitWhenIdle stops the daemon once no session has written anything for
// IdleExit. Closing the app kills its sessions without SessionEnd, so the stop
// hook never runs; the next SessionStart starts a fresh daemon.
func exitWhenIdle() {
	for range time.Tick(time.Minute) {
		if time.Since(lastActivity()) < IdleExit {
			continue
		}
		fmt.Println("⏹ No Claude Code activity for a while, shutting down")
		discordClient.Close()
		// Only when the PID file is still ours, not a newer daemon's
		if data, err := os.ReadFile(pidFilePath); err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(os.Getpid()) {
			os.Remove(pidFilePath)
			os.Remove(refcountFilePath)
		}
		os.Exit(0)
	}
}

// lastActivity is the newest write to any session data source, or the daemon's
// start if that is later.
func lastActivity() time.Time {
	latest := sessionStartTime
	paths := []string{moduleFilePath, dataFilePath}
	if jsonl, _, err := findMostRecentJSONL(); err == nil {
		paths = append(paths, jsonl)
	}
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

func readStatusLineData() *SessionData {
	return readPresenceFile(dataFilePath)
}

// readModuleData reads the file the plugin's hooks module writes. The module
// blanks it to "{}" when its session ends, which reads as no data.
func readModuleData() *SessionData {
	return readPresenceFile(moduleFilePath)
}

func readPresenceFile(path string) *SessionData {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var statusLine StatusLineData
	if err := json.Unmarshal(data, &statusLine); err != nil {
		return nil
	}

	if statusLine.SessionID == "" {
		return nil
	}

	projectPath := statusLine.Workspace.ProjectDir
	if projectPath == "" {
		projectPath = statusLine.Cwd
	}

	projectName := filepath.Base(projectPath)
	if projectName == "" || projectName == "." {
		projectName = "Unknown Project"
	}

	return &SessionData{
		ProjectName: projectName,
		ProjectPath: projectPath,
		GitBranch:   getGitBranch(projectPath),
		ModelName:   statusLine.Model.DisplayName,
		// Context size: the last request's input, cached tokens included.
		TotalTokens: statusLine.ContextWindow.TotalInputTokens,
		WindowSize:  statusLine.ContextWindow.ContextWindowSize,
		UsedPercent: statusLine.ContextWindow.UsedPercentage,
		TotalCost:   statusLine.Cost.TotalCostUSD,
		StartTime:   sessionStartTime,
	}
}

func getGitBranch(projectPath string) string {
	if projectPath == "" {
		return ""
	}

	cmd := exec.Command("git", "-C", projectPath, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	branch := strings.TrimSpace(string(output))

	// If HEAD (no commits yet), try to get the branch name from symbolic-ref
	if branch == "HEAD" {
		cmd = exec.Command("git", "-C", projectPath, "symbolic-ref", "--short", "HEAD")
		output, err = cmd.Output()
		if err == nil {
			branch = strings.TrimSpace(string(output))
		}
	}

	return branch
}

// findMostRecentJSONL finds the most recently modified JSONL file in ~/.claude/projects/
func findMostRecentJSONL() (string, string, error) {
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return "", "", fmt.Errorf("projects directory does not exist")
	}

	type jsonlFile struct {
		path        string
		projectPath string
		modTime     time.Time
	}

	var files []jsonlFile

	err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Extract project path from the directory structure
		// ~/.claude/projects/<encoded-path>/<session>.jsonl
		// Encoded path uses dashes: -Users-vasantpns-Developer-project
		relPath, _ := filepath.Rel(projectsDir, path)
		parts := strings.SplitN(relPath, string(filepath.Separator), 2)
		if len(parts) < 1 {
			return nil
		}

		// Decode the project path
		// Claude Code encodes paths: / becomes -, and literal - becomes --
		// Example: /Users/foo/my-project -> -Users-foo-my--project
		// Must decode -- to - FIRST, then decode single - to /
		encodedPath := parts[0]
		// Use a placeholder for double dashes (escaped literal dashes)
		projectPath := strings.ReplaceAll(encodedPath, "--", "\x00")
		// Convert single dashes to path separators
		projectPath = strings.ReplaceAll(projectPath, "-", "/")
		// Restore literal dashes from placeholder
		projectPath = strings.ReplaceAll(projectPath, "\x00", "-")

		files = append(files, jsonlFile{
			path:        path,
			projectPath: projectPath,
			modTime:     info.ModTime(),
		})

		return nil
	})

	if err != nil {
		return "", "", err
	}

	if len(files) == 0 {
		return "", "", fmt.Errorf("no JSONL files found")
	}

	// Sort by modification time, most recent first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	return files[0].path, files[0].projectPath, nil
}

// parseJSONLSession parses a JSONL file and extracts session data
func parseJSONLSession(jsonlPath, _ string) *SessionData {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var (
		totalInputTokens  int64
		totalOutputTokens int64
		contextTokens     int64
		lastModel         string
		projectPath       string
	)

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var msg JSONLMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		// Extract cwd from any message that has it (usually first message)
		if msg.Cwd != "" && projectPath == "" {
			projectPath = msg.Cwd
		}

		// Only process assistant messages with usage data
		if msg.Type == "assistant" && msg.Message.Model != "" {
			lastModel = msg.Message.Model
			usage := msg.Message.Usage
			totalInputTokens += usage.InputTokens
			totalOutputTokens += usage.OutputTokens
			// Context size: the latest request's input, cached tokens included
			contextTokens = usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		}
	}

	if lastModel == "" {
		return nil
	}

	// Calculate cost based on model pricing
	totalCost := calculateCost(lastModel, totalInputTokens, totalOutputTokens)

	// Get display name for model
	modelName := formatModelName(lastModel)

	projectName := filepath.Base(projectPath)
	if projectName == "" || projectName == "." {
		projectName = "Unknown Project"
	}

	// Use daemon start time for elapsed time display
	// This shows how long Discord presence has been active, not total session time
	return &SessionData{
		ProjectName: projectName,
		ProjectPath: projectPath,
		GitBranch:   getGitBranch(projectPath),
		ModelName:   modelName,
		TotalTokens: contextTokens,
		TotalCost:   totalCost,
		StartTime:   sessionStartTime,
	}
}

// calculateCost calculates the cost based on token usage and model pricing
func calculateCost(modelID string, inputTokens, outputTokens int64) float64 {
	pricing, ok := modelPricing[modelID]
	if !ok {
		// Default to Sonnet 4 pricing if unknown model
		pricing = modelPricing["claude-sonnet-4-20250514"]
	}

	inputCost := float64(inputTokens) / 1_000_000 * pricing.Input
	outputCost := float64(outputTokens) / 1_000_000 * pricing.Output

	return inputCost + outputCost
}

// formatModelName converts model ID to display name
func formatModelName(modelID string) string {
	if name, ok := modelDisplayNames[modelID]; ok {
		return name
	}

	// Try to extract a reasonable name from the model ID
	if strings.Contains(modelID, "opus") {
		return "Opus"
	}
	if strings.Contains(modelID, "sonnet") {
		return "Sonnet"
	}
	if strings.Contains(modelID, "haiku") {
		return "Haiku"
	}

	return "Claude"
}

// readSessionData tries statusline data first, then falls back to JSONL parsing
func readSessionData() *SessionData {
	// The hooks module first: it runs in every host that loads it, the
	// desktop app included. Then the statusline wrapper, for Claude Code
	// versions without hooks modules. Then JSONL parsing as a last resort.
	if data := readModuleData(); data != nil {
		setSource(sourceModule)
		return data
	}
	if data := readStatusLineData(); data != nil {
		setSource(sourceStatusLine)
		return data
	}

	jsonlPath, projectPath, err := findMostRecentJSONL()
	if err != nil {
		return nil
	}

	setSource(sourceJSONL)
	if !nudgeShown {
		nudgeShown = true
		fmt.Println("\n💡 Tip: For accurate token/cost data, update Claude Code (the plugin's hooks module")
		fmt.Println("   needs a recent version) or configure the statusline wrapper.")
		fmt.Println("   See: https://github.com/anaspxr/vibe-clauding-discord-presence#statusline-setup")
	}

	return parseJSONLSession(jsonlPath, projectPath)
}

// setSource logs when the data source changes after startup.
func setSource(source string) {
	if currentSource != "" && currentSource != source {
		fmt.Printf("📊 Now using %s\n", source)
	}
	currentSource = source
}

// presenceConfig is ~/.claude/discord-presence-config.json. Every field is
// optional; an empty one keeps the built-in text. Templates may use
// {project} {branch} {model} {tokens} {window} {percent} {cost}.
type presenceConfig struct {
	Details  string `json:"details"`
	State    string `json:"state"`
	Activity string `json:"activity"` // a key of activityTypes; "playing" otherwise
	// The third line (Discord's image text). A pointer so "" can hide it.
	LargeText *string `json:"large_text"`
}

// activityTypes are the Discord activity types RPC accepts, by the word
// Discord shows before the app name.
var activityTypes = map[string]int{"playing": 0, "listening": 2, "watching": 3, "competing": 5}

// loadConfig is read on every update, so edits apply without a restart.
func loadConfig() presenceConfig {
	var cfg presenceConfig
	if data, err := os.ReadFile(configFilePath); err == nil {
		json.Unmarshal(data, &cfg) // bad JSON keeps the defaults
	}
	return cfg
}

// presenceText builds the three activity lines Discord shows.
func presenceText(session *SessionData) (details, state, largeText string) {
	details = fmt.Sprintf("Working on: %s", session.ProjectName)
	if session.GitBranch != "" {
		details = fmt.Sprintf("Working on: %s (%s)", session.ProjectName, session.GitBranch)
	}

	// model | context size | cost
	state = fmt.Sprintf("%s | %s tokens | $%.4f",
		session.ModelName,
		formatNumber(session.TotalTokens),
		session.TotalCost)

	cfg := loadConfig()
	fill := strings.NewReplacer(
		"{project}", session.ProjectName,
		"{branch}", session.GitBranch,
		"{model}", session.ModelName,
		"{tokens}", formatNumber(session.TotalTokens),
		"{window}", formatNumber(session.WindowSize),
		"{percent}", fmt.Sprintf("%.0f%%", session.UsedPercent),
		"{cost}", fmt.Sprintf("$%.4f", session.TotalCost),
	)
	if cfg.Details != "" {
		details = fill.Replace(cfg.Details)
	}
	if cfg.State != "" {
		state = fill.Replace(cfg.State)
	}
	largeText = "Vibe Clauding - Discord Rich Presence for Claude Code"
	if cfg.LargeText != nil {
		largeText = fill.Replace(*cfg.LargeText)
	}
	return details, state, largeText
}

func updatePresence(session *SessionData) {
	details, state, largeText := presenceText(session)
	activityType := activityTypes[strings.ToLower(loadConfig().Activity)]

	// The data files change often (the hooks module writes after every tool
	// call) and the poll runs every few seconds; only send what Discord
	// would show differently.
	key := fmt.Sprintf("%s\n%s\n%s\n%d", details, state, session.StartTime, activityType)
	if key == lastPresence {
		return
	}

	if err := discordClient.SetActivity(discord.Activity{
		Details:   details,
		State:     state,
		LargeText: largeText,
		StartTime: &session.StartTime,
		Type:      activityType,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error updating presence: %v\n", err)
		return
	}
	lastPresence = key
}

func formatNumber(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	} else if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func watchForChanges() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Println("Using polling mode for session tracking")
		pollForChanges()
		return
	}
	defer watcher.Close()

	// Watch the main claude dir (module and statusline data files)
	if err := watcher.Add(claudeDir); err != nil {
		fmt.Println("Using polling mode for session tracking")
		pollForChanges()
		return
	}

	// Also poll as backup (especially important for JSONL which is in subdirs)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Respond to module and statusline data file changes
			if name := filepath.Base(event.Name); name == filepath.Base(moduleFilePath) || name == filepath.Base(dataFilePath) {
				if session := readSessionData(); session != nil {
					updatePresence(session)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "Watcher error: %v\n", err)
		case <-ticker.C:
			// Poll reads from either statusline or JSONL fallback
			if session := readSessionData(); session != nil {
				updatePresence(session)
			}
		}
	}
}

func pollForChanges() {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for range ticker.C {
		if session := readSessionData(); session != nil {
			updatePresence(session)
		}
	}
}
