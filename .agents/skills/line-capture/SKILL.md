---
name: line-capture
description: Launches Chrome with network capture (CDP recorder plus net-log) to record LINE Chrome Extension traffic for the beeper/line Matrix bridge, after checking the dependencies. Use when you need raw LINE Chrome Extension traffic for a feature, or when asked to capture, record or sniff LINE traffic.
---

# LINE Capture

Records what the real LINE Chrome Extension sends, so the bridge can copy it.
Run the commands below from the repository root.

## What to do

1. **Check dependencies.** Run `bash .agents/skills/line-capture/scripts/line-capture-preflight.sh`
   (on Windows, from Git Bash). It prints the detected OS and an `OK` or
   `MISSING` line per dependency. It does not say how to install anything:
   choose the install method that fits the detected OS and the package managers
   that are actually available (for example Homebrew on macOS; apt, dnf or
   pacman on Linux; winget, Chocolatey or Scoop on Windows; `<python> -m pip
   install --user` for Python packages, using the Python the preflight
   reported). Show the user the missing items with the exact commands you plan
   to run, ask them to confirm, then install and re-run the preflight until it
   exits 0. Never install without that confirmation. The launcher refuses to
   start while dependencies are missing.

2. **Close any running Chrome instances first.** Chrome only supports one
   instance per profile with remote debugging. Warn the user if Chrome is
   running.

3. Run the capture script from the checkout:

   ```bash
   bash .agents/skills/line-capture/scripts/line-chrome-capture.sh /tmp/line-chrome-extension-capture
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
   - If the LINE extension is missing and the user asked for setup, relaunch
     with `LINE_CAPTURE_AUTO_LOAD_EXTENSIONS=0`, open the official Web Store
     listing for `ophjlpahpchlmihnnnihgmmeilfjmjjc`, verify LINE / LY
     Corporation, click Add to Chrome, and accept the standard "Add extension"
     confirmation

4. Tell the user:
   - Chrome opens on `about:blank`. Open LINE from the extension's toolbar icon
     (puzzle-piece menu if it is not pinned), not by opening the extension's
     `index.html` as a tab: that behaves differently from the real popup.
     `LINE_CAPTURE_START_URL` overrides the start URL.
   - Log in manually if the capture profile is not already logged in
   - Perform whatever flow they want to analyze
   - Do not open DevTools on the extension: LINE has `debugger;` traps that
     pause it, and the recorder does not need DevTools
   - Windows the extension opens (Settings, group popups) can start frozen
     and blank, with LINE greyed out as "Debugger paused in another tab". The
     recorder releases them automatically. If one still freezes, quit Chrome
     and run the capture again.
   - When done, quit Chrome completely (all windows, not only the tab)

5. Once Chrome is closed, the capture files are ready. Continue with the
   `line-analyze` skill.

Raw captures are secrets (tokens, cookies, MIDs, message bodies). Never commit
them. See the `line-implement` skill for the full security rules.
