import express from 'express';
import fetch from 'node-fetch';
import http from 'http';
import { WebSocketServer, WebSocket } from 'ws';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const DEFAULT_RELAY_HOST = '127.0.0.1';
const DEFAULT_RELAY_PORT = 8787;

const app = express();
app.use(express.static(path.join(__dirname, 'public')));

app.get('/sessions', async (req, res) => {
  const { key, host = DEFAULT_RELAY_HOST, port = DEFAULT_RELAY_PORT } = req.query;
  console.log(`[viewer] sessions request host=${host} port=${port}`);
  if (!key) {
    return res.status(400).json({ error: 'Missing key query parameter' });
  }

  const baseUrl = `http://${host}:${port}`;
  try {
    const response = await fetch(`${baseUrl}/api/v1/sessions`, {
      headers: { 'X-API-Key': key },
    });
    if (!response.ok) {
      const text = await response.text();
      console.error(`[viewer] relay list failed status=${response.status} body=${text}`);
      return res.status(response.status).json({ error: text || 'Relay request failed' });
    }
    const data = await response.json();
    console.log(`[viewer] relay sessions count=${(data.sessions || []).length}`);
    return res.json({ baseUrl, sessions: data.sessions ?? [] });
  } catch (error) {
    console.error(`[viewer] sessions error ${error.message}`);
    return res.status(500).json({ error: (error instanceof Error ? error.message : String(error)) });
  }
});

const server = http.createServer(app);
const wss = new WebSocketServer({ noServer: true });

wss.on('connection', (socket, request, clientMeta) => {
  const { apiKey, host, port, sessionId } = clientMeta;
  const relayUrl = `ws://${host}:${port}/terminal`;
  console.log(`[viewer] ws proxy connecting -> ${relayUrl} session=${sessionId}`);

  const upstream = new WebSocket(relayUrl, {
    headers: { 'X-API-Key': apiKey },
  });

  const teardown = (code = 1000, reason = 'closing') => {
    try {
      if (upstream.readyState === WebSocket.OPEN) {
        upstream.close(code, reason);
      }
    } catch (_) {}
    try {
      if (socket.readyState === WebSocket.OPEN) {
        socket.close(code, reason);
      }
    } catch (_) {}
  };

  upstream.on('open', () => {
    console.log(`[viewer] upstream open session=${sessionId}`);
    const payload = JSON.stringify({ type: 'join_session', session_id: sessionId });
    upstream.send(payload);
  });

  upstream.on('message', (data, isBinary) => {
    console.log(
      `[viewer] upstream msg len=${isBinary ? data.length : Buffer.byteLength(String(data))} binary=${isBinary}`
    );
    if (socket.readyState === WebSocket.OPEN) {
      socket.send(data, { binary: isBinary });
    }
  });

  upstream.on('close', (code, reason) => {
    console.log(`[viewer] upstream close code=${code} reason=${reason}`);
    teardown(code, reason.toString());
  });

  upstream.on('error', (err) => {
    console.error(`[viewer] upstream error ${err.message}`);
    socket.send(JSON.stringify({ type: 'error', message: err.message }));
    teardown(1011, 'upstream error');
  });

  socket.on('message', (data, isBinary) => {
    if (upstream.readyState === WebSocket.OPEN) {
      upstream.send(data, { binary: isBinary });
      console.log(`[viewer] client message forwarded bytes=${isBinary ? data.length : Buffer.byteLength(data.toString())}`);
    }
  });

  socket.on('close', (code, reason) => {
    console.log(`[viewer] client close code=${code} reason=${reason}`);
    teardown(code, reason.toString());
  });

  socket.on('error', () => {
    console.error('[viewer] client socket error');
    teardown(1011, 'client error');
  });
});

server.on('upgrade', (request, socket, head) => {
  const url = new URL(request.url, 'http://localhost');
  if (url.pathname !== '/ws') {
    socket.destroy();
    return;
  }

  const apiKey = url.searchParams.get('key');
  const sessionId = url.searchParams.get('sessionId');
  const host = url.searchParams.get('host') ?? DEFAULT_RELAY_HOST;
  const port = url.searchParams.get('port') ?? DEFAULT_RELAY_PORT;

  if (!apiKey || !sessionId) {
    socket.destroy();
    return;
  }

  wss.handleUpgrade(request, socket, head, (ws) => {
    wss.emit('connection', ws, request, { apiKey, sessionId, host, port });
  });
});

const PORT = process.env.PORT || 4173;
server.listen(PORT, () => {
  console.log(`Relay viewer listening on http://localhost:${PORT}`);
});
