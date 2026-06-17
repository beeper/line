Launch Chrome with network capture enabled for LINE Chrome Extension analysis.

## What to do

1. **Close any running Chrome instances first** (Chrome only supports one instance per profile with remote debugging). Warn the user if Chrome is running.

2. Run the capture script:
   ```
   bash scripts/line-chrome-capture.sh /tmp/line-chrome-extension-capture
   ```
   This launches Chrome with:
   - Remote debugging on port 9222
   - Full network logging to `/tmp/line-chrome-extension-capture/line-net-log.json`
   - A persistent capture-only Chrome profile at
     `~/.cache/line-chrome-extension-capture/chrome-debug-profile`
   - Auto-loaded LINE and Codex extensions staged from the user's normal Chrome
     profile into `~/.cache/line-chrome-extension-capture/extensions`
   - A LINE extension version preflight:
     - repo version from `pkg/line/client.go` `ExtensionVersion`
     - installed/staged Chrome extension version from `manifest.json`
     - a warning if those versions differ
   - First-run Chrome prompts pre-skipped:
     - "Make Google Chrome the default browser" stays unchecked
     - "Automatically send usage statistics and crash reports" stays unchecked
     - Chrome/Sync sign-in is skipped; use "Stay Signed Out" if it appears
  - If the LINE extension is missing and the user asked for setup, relaunch with
    `LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0`, open the official Web Store listing
    for `ophjlpahpchlmihnnnihgmmeilfjmjjc`, verify LINE / LY Corporation, click
    Add to Chrome, and accept the standard "Add extension" confirmation

3. Tell the user:
   - Open the LINE Chrome Extension (browser toolbar icon)
   - Log in manually if the capture profile is not already logged in
   - Perform whatever flow they want to analyze
   - When done, quit Chrome completely (Cmd+Q)

4. Once Chrome is closed, the net-log file is ready. Continue with the
   line-analyze workflow.
