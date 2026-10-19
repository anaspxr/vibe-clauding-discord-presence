# Vibe Clauding - Discord Rich Presence for Claude Code

Show your Claude Code session on Discord! Display your current project, git branch, model, session time, token usage, and cost in real-time.

> **This is a fork of [tsanva/cc-discord-presence](https://github.com/tsanva/cc-discord-presence)** by [@tsanva](https://github.com/tsanva), released under the same MIT license. All the session tracking and Discord integration is their work. This fork adds [customizable status text](#customizing-the-status-text) and runs under its own Discord app. If you want the original, go to the [upstream repo](https://github.com/tsanva/cc-discord-presence).

## Platform Support

| Platform              | Status      |
| --------------------- | ----------- |
| macOS (Apple Silicon) | ✅ Tested   |
| macOS (Intel)         | ⚠️ Untested |
| Linux (x64)           | ⚠️ Untested |
| Linux (ARM64)         | ⚠️ Untested |
| Windows (x64)         | ✅ Tested   |
| Windows (ARM64)       | ⚠️ Untested |

> **Note**: macOS Intel and Linux should work but haven't been verified. Please [report problems](https://github.com/anaspxr/vibe-clauding-discord-presence/issues). For problems that also happen in the original, [upstream](https://github.com/tsanva/cc-discord-presence/issues) may know more.
>
> **Windows users**: Requires [Git Bash](https://git-scm.com/downloads) (included with Git for Windows) for automatic plugin hooks. Alternatively, run the PowerShell scripts manually (`scripts/start.ps1` and `scripts/stop.ps1`). WSL won't work as Discord runs on the Windows host.

## Features

- **Session Time** - Shows how long you've been coding with Claude
- **Project Name** - Displays the current project you're working on
- **Git Branch** - Shows your current git branch
- **Model Name** - Shows which Claude model you're using (Opus 4.5, Sonnet 4.5, Haiku 4.5)
- **Context Size** - Tokens in Claude's context window right now (cached tokens included)
- **Total Cost** - Real-time cost tracking for your session
- **Custom Status Text** - Change what Discord shows with a small JSON file (this fork's addition)

## Installation

### As a Claude Code Plugin (Recommended)

```bash
# Add the marketplace
claude plugin marketplace add anaspxr/vibe-clauding-discord-presence

# Install the plugin
claude plugin install vibe-clauding-discord-presence@vibe-clauding-discord-presence
```

That's it! The plugin will automatically start when you begin a Claude Code session and stop when you exit.

### Updating

```bash
claude plugin marketplace update vibe-clauding-discord-presence
claude plugin update vibe-clauding-discord-presence@vibe-clauding-discord-presence
```

Then start a new Claude Code session. The plugin's start script notices that the daemon in `~/.claude/bin/` is from an older release, stops it, downloads the matching one and starts it.

> **Upgrading from 1.0.4 or earlier:** `claude plugin update` only replaces the plugin's own files. The daemon binary lives in `~/.claude/bin/` and, before 1.0.5, was downloaded once and never updated, so you may still be running the daemon from your first install. Updating to 1.0.5 fixes this automatically on your next session start. If Discord still doesn't show your project afterwards, delete `~/.claude/bin/cc-discord-presence-*` and start a new session. If you had set up the statusline integration, it keeps working, but on current Claude Code it's no longer needed.

### Manual Installation

```bash
# Clone and build
git clone https://github.com/anaspxr/vibe-clauding-discord-presence.git
cd vibe-clauding-discord-presence
go build -o cc-discord-presence .

# Run manually
./cc-discord-presence
```

## How It Works

The app reads session data from three sources, using the first one available:

### 1. Hooks Module (Zero Config, Most Accurate)

The plugin ships a Claude Code hooks module (`hooks/register.ts`) that writes the session's project, model, context size and cost to `~/.claude/discord-presence-module.json` using Claude Code's own figures. It runs in the terminal, the Claude desktop app and IDE extensions on Claude Code versions with hooks module support, and needs no setup. It updates when a session starts, when you send a prompt, after each tool call, after each response, and clears its data when the session ends.

### 2. Statusline Integration (Older Claude Code Versions)

On Claude Code versions without hooks modules, you can configure the statusline integration for the same accuracy. On current versions it isn't needed.

### 3. JSONL Fallback

As a last resort, the app parses Claude Code's session files from `~/.claude/projects/`. Cost is estimated from the model pricing table.

<a name="statusline-setup"></a>

#### Statusline Setup (Optional)

Only needed on Claude Code versions without hooks module support.

**Automatic Setup (Recommended)**:

Run the setup script (requires `jq`):

```bash
# Find your plugin directory and run setup
~/.claude/plugins/cache/*/vibe-clauding-discord-presence/*/scripts/setup-statusline.sh
```

Or if you have the repo cloned:

```bash
./scripts/setup-statusline.sh
```

The setup script will:

- Copy `statusline-wrapper.sh` to `~/.claude/`
- Update your `~/.claude/settings.json` automatically
- Back up any existing statusline to `~/.claude/statusline.sh`

**Manual Setup**: If you prefer, edit `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "command": "~/.claude/statusline-wrapper.sh",
    "type": "command"
  }
}
```

Then copy `scripts/statusline-wrapper.sh` to `~/.claude/statusline-wrapper.sh`.

**Note**: Restart Claude Code after setup for changes to take effect.

#### Verifying Your Setup

Check which data source is being used by viewing the daemon log:

```bash
cat ~/.claude/discord-presence.log
```

You'll see one of:

- `✓ Found active session: project-name (using hooks module)` - Best accuracy, no setup
- `✓ Found active session: project-name (using statusline data)` - Same accuracy, for older Claude Code versions
- `✓ Found active session: project-name (using JSONL fallback)` - Working, but cost is estimated; update Claude Code or set up the statusline

## Discord Presence Display

```
┌─────────────────────────────────┐
│ Vibe Clauding                      │
│ Working on: my-project (main)   │
│ Opus 4.5 | 1.5M tokens | $0.1234│
│ 00:45:30 elapsed                │
└─────────────────────────────────┘
```

## Requirements

- [Discord](https://discord.com) desktop app running
- [Claude Code](https://claude.ai/code) installed
- Go 1.25+ (only for building from source)

## Building from Source

```bash
# Build for current platform
go build -o cc-discord-presence .

# Cross-compile for all platforms
mkdir -p bin
GOOS=darwin GOARCH=arm64 go build -o bin/cc-discord-presence-darwin-arm64 .
GOOS=darwin GOARCH=amd64 go build -o bin/cc-discord-presence-darwin-amd64 .
GOOS=linux GOARCH=amd64 go build -o bin/cc-discord-presence-linux-amd64 .
GOOS=linux GOARCH=arm64 go build -o bin/cc-discord-presence-linux-arm64 .
GOOS=windows GOARCH=amd64 go build -o bin/cc-discord-presence-windows-amd64.exe .
```

## Token Pricing

Cost is calculated using current Claude API pricing (Dec 2025):

| Model      | Input (per 1M tokens) | Output (per 1M tokens) |
| ---------- | --------------------- | ---------------------- |
| Opus 4.5   | $15.00                | $75.00                 |
| Sonnet 4.5 | $3.00                 | $15.00                 |
| Sonnet 4   | $3.00                 | $15.00                 |
| Haiku 4.5  | $1.00                 | $5.00                  |

## Customizing the Status Text

Discord shows two text lines under the app name: `details` (top) and `state` (bottom). Create `~/.claude/discord-presence-config.json` to change them:

```json
{
  "details": "Token-Maxing🔥 {model} | {tokens} tokens",
  "state": "{project}"
}
```

Both fields are optional. A field you leave out keeps the built-in text. Available placeholders:

| Placeholder | Value                                                  |
| ----------- | ------------------------------------------------------ |
| `{project}` | Project folder name                                    |
| `{branch}`  | Git branch (empty if the folder isn't a git repo)      |
| `{model}`   | Model name                                             |
| `{tokens}`  | Context size, e.g. `54.1K`                             |
| `{window}`  | Model's context window, e.g. `1.0M` (hooks module or statusline only) |
| `{percent}` | Context used, e.g. `15%` (hooks module or statusline only) |
| `{cost}`    | Session cost, e.g. `$1.2700`                           |

Add `"large_text"` to change the third line (also the app icon's hover text), with the same placeholders; `""` hides it.

Add `"activity"` to change the word Discord shows before the app name: `"playing"` (the default), `"listening"`, `"watching"` or `"competing"`. Discord shows only one activity of each kind, so another app that also shows "Playing" (a VS Code extension, a game) can hide this one; `"competing"` keeps them apart.

The file is re-read every few seconds, so edits show up without restarting. If the file is missing or isn't valid JSON, the defaults are used. Discord cuts off long lines, so keep them short. [discord-presence-config.example.json](discord-presence-config.example.json) is a ready-to-copy example.

## Advanced: Custom Discord App

This fork uses its own Discord application ("Vibe Clauding"). If you want your own name and icon:

1. Go to [Discord Developer Portal](https://discord.com/developers/applications)
2. Click "New Application" and name it
   > ⚠️ **Note**: Discord blocks trademarked names like "Claude Code"
3. Set an app icon in "General Information" (this appears in Rich Presence)
4. Copy the **Application ID** and update `ClientID` in `main.go`
5. Rebuild the binary

## Uninstallation

### Plugin Removal

```bash
claude plugin uninstall vibe-clauding-discord-presence@vibe-clauding-discord-presence
```

### Statusline Cleanup (if configured)

If you set up statusline integration, restore your original settings:

```bash
# Remove the wrapper script
rm ~/.claude/statusline-wrapper.sh

# Restore your original statusline in settings.json:
# Option 1: Point back to the default statusline.sh
jq '.statusLine.command = "~/.claude/statusline.sh"' ~/.claude/settings.json > ~/.claude/settings.json.tmp \
  && mv ~/.claude/settings.json.tmp ~/.claude/settings.json

# Option 2: Remove statusline config entirely
jq 'del(.statusLine)' ~/.claude/settings.json > ~/.claude/settings.json.tmp \
  && mv ~/.claude/settings.json.tmp ~/.claude/settings.json
```

Restart Claude Code after making changes.

## Privacy

This application runs entirely locally and does not collect any data. See [PRIVACY.md](PRIVACY.md) for details.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

**Before submitting a PR**, make sure to run the test suite:

```bash
go test -v ./...
```

## Acknowledgments

- [Anthropic](https://anthropic.com) for Claude
- [fsnotify](https://github.com/fsnotify/fsnotify) for file watching
- [go-winio](https://github.com/Microsoft/go-winio) for Windows named pipe support
- The Claude Code community
