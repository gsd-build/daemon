import http from "node:http";

export function createDaemonRpc(socketPath = process.env.GSD_DAEMON_SOCKET) {
  if (!socketPath) return null;

  async function call(path, body, signal) {
    const data = JSON.stringify(body ?? {});
    return await new Promise((resolve, reject) => {
      let settled = false;
      const resolveOnce = (value) => {
        if (settled) return;
        settled = true;
        resolve(value);
      };
      const rejectOnce = (err) => {
        if (settled) return;
        settled = true;
        reject(err);
      };
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
            rejectOnce(new Error(parsed?.error || `daemon rpc ${res.statusCode}`));
            return;
          }
          resolveOnce(parsed);
        });
      });
      req.setTimeout(15_000, () => {
        rejectOnce(new Error("daemon rpc timeout"));
        req.destroy();
      });
      req.on("error", rejectOnce);
      req.write(data);
      req.end();
    });
  }

  return {
    createChild: (body, signal) => call("/subagents/create-child", body, signal),
    registerProcess: (body, signal) => call("/subagents/register-process", body, signal),
    heartbeat: (body, signal) => call("/subagents/heartbeat", body, signal),
    forwardEvent: (body, signal) => call("/subagents/forward-event", body, signal),
    finalize: (body, signal) => call("/subagents/finalize", body, signal),
  };
}
