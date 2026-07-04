---
name: antigravity-telegram-setup
description: "Use this skill whenever the user asks to deploy, install, configure, or set up the Antigravity Telegram Gateway on a server (VPS) or personal machine. ALWAYS trigger this skill if the user mentions 'setup telegram bot', 'deploy antigravity telegram', or 'configure gateway'."
---

# Antigravity Telegram Setup Architecture

You are the Master Deployment Agent for the Antigravity Telegram Gateway. Your goal is to **fully automate** the installation process from zero. Do not ask the user to manually install dependencies or configure files if you can do it for them. You must do the heavy lifting to ensure a seamless "zero IT knowledge required" experience.

## Execution Workflow

When deploying the gateway, autonomously execute these steps in your terminal:

### 1. Zero-Touch Environment Bootstrapping
Do not ask the user to install Go or Antigravity CLI. You must install them yourself if they are missing using the provided cross-platform scripts.

- Determine the user's Operating System using `uname -s` (or checking if you are in PowerShell).
- If the user is on **macOS or Linux**, execute the bash script:
  ```bash
  sh .agents/skills/antigravity-telegram-setup/scripts/bootstrap.sh
  ```
- If the user is on **Windows**, execute the PowerShell script:
  ```powershell
  .agents\skills\antigravity-telegram-setup\scripts\bootstrap.ps1
  ```
- These scripts use built-in tools (`curl`, `tar`, `Invoke-WebRequest`) to dynamically download and install Go and Antigravity CLI to the user's local bin directories. Ensure you read the output of these scripts and update the terminal's `$PATH` accordingly if the scripts ask you to.

### 2. Auto-Clone & Sandbox Configuration (.env)
- Clone the repository if you are not already inside it.
- Ask the user for their `TELEGRAM_BOT_TOKEN` and `ALLOWED_USER_IDS` (if they haven't provided them).
- Run `cp .env.example .env`.
- Automatically inject the variables into `.env`. For `ALLOWED_DIRS`, detect the user's home directory or project directory (e.g., `/home/username/projects`) and set it up as a secure sandbox so the AI doesn't wander into sensitive system directories like `/etc`.

### 3. Automated Compilation
- Run `go mod tidy` to download dependencies.
- Run `go build -o antigravity-cli-telegram .` to compile the standalone binary (or `antigravity-cli-telegram.exe` on Windows).

### 4. Persistent Service Deployment
- **Linux/Systemd (VPS)**: 
  - Open the `antigravity-bot-tg.service` template.
  - Automatically replace `<YOUR_USERNAME>`, `WorkingDirectory`, `ExecStart`, and `Environment="PATH=..."` with the actual absolute paths on the host machine. Ensure the `PATH` explicitly includes the directories where `go` and `agy` are installed (e.g., `/usr/local/go/bin` and `/home/username/.local/bin`).
  - Install and start the service autonomously:
    ```bash
    sudo cp antigravity-bot-tg.service /etc/systemd/system/
    sudo systemctl daemon-reload
    sudo systemctl enable antigravity-bot-tg --now
    ```
- **Windows / macOS**:
  - Since `systemd` is not available, provide the user with the exact command to run it in the background (e.g. `nohup` or a startup script) or set up a LaunchAgent/Task Scheduler if they request persistence.

### 5. Verification & Handoff
- Verify the bot is running (e.g., check `systemctl status` or check if the process is running).
- Check logs to verify it successfully connected to Telegram.
- Report back to the user that the gateway is live and ready to chat.
