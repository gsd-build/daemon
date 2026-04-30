import http from "node:http";

export function createDaemonRpc(socketPath = process.env.GSD_DAEMON_SOCKET) {
  if (!socketPath) return null;

  async function call(path, body, signal) {
    const data = JSON.stringify(body ?? {});
    return await new Promise((resolve, reject) => {
      const req = http.request({
        socketPath,
        path,
        method: "POST",
        headers: {
          "content-type": "application/json",
          "content-length": Buffer.byteLength(data),
        },
        signal,
      }, (res) => {
        let text = "";
        res.setEncoding("utf8");
        res.on("data", (chunk) => {
          text += chunk;
        });
        res.on("end", () => {
          let parsed = {};
          if (text) {
            try {
              parsed = JSON.parse(text);
            } catch {
              parsed = { error: text };
            }
          }
          if (res.statusCode && res.statusCode >= 400) {
            reject(new Error(parsed?.error || `daemon rpc ${res.statusCode}`));
            return;
          }
          resolve(parsed);
        });
      });
      req.on("error", reject);
      req.write(data);
      req.end();
    });
  }

  return {
    createChild: (body, signal) => call("/subagents/create-child", body, signal),
    registerProcess: (body, signal) => call("/subagents/register-process", body, signal),
    forwardEvent: (body, signal) => call("/subagents/forward-event", body, signal),
    finalize: (body, signal) => call("/subagents/finalize", body, signal),
  };
}
