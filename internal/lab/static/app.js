const state = { ws: null, channelId: crypto.randomUUID(), sessionId: crypto.randomUUID(), task: null, terminalId: null };
const messages = document.getElementById("messages");
const events = document.getElementById("events");
const form = document.getElementById("composer");
const promptInput = document.getElementById("prompt");

function appendMessage(role, text) {
  const node = document.createElement("div");
  node.className = "message";
  node.textContent = `${role}: ${text}`;
  messages.appendChild(node);
  messages.scrollTop = messages.scrollHeight;
}

function appendEvent(event) {
  events.textContent += `${JSON.stringify(event)}\n`;
  events.scrollTop = events.scrollHeight;
}

async function loadConfig() {
  const response = await fetch("/api/config");
  const config = await response.json();
  document.getElementById("cwd").value = config.cwd || "";
  setSelectValue(document.getElementById("provider"), config.provider || "claude-cli");
  document.getElementById("model").value = config.model || "claude-sonnet-4-6";
  setSelectValue(document.getElementById("effort"), config.effort || "medium");
  setSelectValue(document.getElementById("permissionMode"), config.permissionMode || "acceptEdits");
  await loadFiles(config.cwd || "");
}

function setSelectValue(select, value) {
  if (!Array.from(select.options).some((option) => option.value === value)) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = value;
    select.appendChild(option);
  }
  select.value = value;
}

async function loadFiles(path) {
  const query = path ? `?path=${encodeURIComponent(path)}` : "";
  const response = await fetch(`/api/browse${query}`);
  const result = await response.json();
  const tree = document.getElementById("fileTree");
  tree.textContent = "";
  for (const entry of result.entries || []) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = `${entry.isDirectory ? "Dir" : "File"} ${entry.name}`;
    button.addEventListener("click", async () => {
      if (entry.isDirectory) {
        await loadFiles(entry.path);
        return;
      }
      const fileResponse = await fetch(`/api/file?path=${encodeURIComponent(entry.path)}`);
      const file = await fileResponse.json();
      document.getElementById("filePreview").textContent = file.content || file.error || "";
    });
    tree.appendChild(button);
  }
}

function connect() {
  const wsURL = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws/ui`;
  state.ws = new WebSocket(wsURL);
  state.ws.onmessage = (message) => {
    const event = JSON.parse(message.data);
    appendEvent(event);
    if (event.type === "stream" && event.event?.type === "message_update") appendMessage("assistant", JSON.stringify(event.event));
    if (event.type === "taskError") appendMessage("error", event.error);
    if (event.type === "question") renderQuestion(event);
    if (event.type === "terminalOutput" || event.type === "terminalSnapshot") {
      document.getElementById("terminalOutput").textContent += atob(event.dataBase64 || "");
    }
    if (event.type === "agentTerminalStarted" || event.type === "agentTerminalUpdated") {
      document.getElementById("terminalOutput").textContent += `${JSON.stringify(event)}\n`;
    }
  };
}

function sendTask(prompt) {
  state.task = crypto.randomUUID();
  const payload = {
    type: "task",
    taskId: state.task,
    sessionId: state.sessionId,
    channelId: state.channelId,
    prompt,
    engine: "pi",
    provider: document.getElementById("provider").value,
    model: document.getElementById("model").value,
    effort: document.getElementById("effort").value,
    permissionMode: document.getElementById("permissionMode").value,
    cwd: document.getElementById("cwd").value || "."
  };
  state.ws.send(JSON.stringify(payload));
  appendEvent(payload);
  appendMessage("user", prompt);
}

function renderQuestion(event) {
  const answer = window.prompt(event.question || event.header || "Question");
  if (answer !== null) {
    const payload = {
      type: "questionResponse",
      channelId: event.channelId,
      sessionId: event.sessionId,
      requestId: event.requestId,
      answer
    };
    state.ws.send(JSON.stringify(payload));
    appendEvent(payload);
  }
}

function sendTerminalOpen() {
  state.terminalId = crypto.randomUUID();
  const payload = {
    type: "terminalOpen",
    terminalId: state.terminalId,
    sessionId: state.sessionId,
    channelId: state.channelId,
    cwd: document.getElementById("cwd").value || ".",
    cols: 100,
    rows: 24
  };
  state.ws.send(JSON.stringify(payload));
  appendEvent(payload);
}

function sendTerminalInput(terminalId, text) {
  const payload = {
    type: "terminalInput",
    terminalId,
    channelId: state.channelId,
    dataBase64: btoa(text)
  };
  state.ws.send(JSON.stringify(payload));
  appendEvent(payload);
}

form.addEventListener("submit", (event) => {
  event.preventDefault();
  const prompt = promptInput.value.trim();
  if (!prompt) return;
  promptInput.value = "";
  sendTask(prompt);
});

document.getElementById("terminalBtn").addEventListener("click", () => sendTerminalOpen());

document.getElementById("exportBtn").addEventListener("click", async () => {
  const response = await fetch("/api/export");
  const bundle = await response.json();
  const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "provider-lab-export.json";
  a.click();
  URL.revokeObjectURL(a.href);
});

loadConfig();
connect();
