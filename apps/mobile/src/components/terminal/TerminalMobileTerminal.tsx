import React, {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import {
  ActivityIndicator,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import WebView, { WebViewMessageEvent } from 'react-native-webview';
import type { WebView as WebViewType } from 'react-native-webview';

import { theme } from '@/constants/theme';
import { supabase } from '@/lib/supabase';
import { reportError } from '@/lib/logger';

interface RelayConfig {
  host: string;
  port: number;
  secure: boolean;
  baseHttpUrl: string;
  baseWsUrl: string;
}

type TerminalStatus =
  | 'idle'
  | 'preparing'
  | 'checking-session'
  | 'session-missing'
  | 'connecting'
  | 'connected'
  | 'ended'
  | 'disconnected'
  | 'error';

interface InitMessagePayload {
  instanceId: string;
  accessToken: string;
  relayConfig: RelayConfig;
  supabaseSubprotocolPrefix: string;
}

interface WebViewMessage {
  type?: string;
  status?: TerminalStatus;
  message?: string;
}

const SUPABASE_SUBPROTOCOL_PREFIX = 'omnara-supabase.';

function buildRelayConfig(): RelayConfig {
  const host = process.env.EXPO_PUBLIC_RELAY_HOST || '127.0.0.1';
  const portRaw = process.env.EXPO_PUBLIC_RELAY_PORT || '8787';
  const secure =
    (process.env.EXPO_PUBLIC_RELAY_SECURE || 'false').toLowerCase() === 'true';

  const parsedPort = Number.parseInt(portRaw, 10);
  const port = Number.isFinite(parsedPort) && parsedPort > 0 ? parsedPort : 0;
  const httpScheme = secure ? 'https' : 'http';
  const wsScheme = secure ? 'wss' : 'ws';
  const baseHost = port > 0 ? `${host}:${port}` : host;

  return {
    host,
    port,
    secure,
    baseHttpUrl: `${httpScheme}://${baseHost}`,
    baseWsUrl: `${wsScheme}://${baseHost}`,
  };
}

function buildTerminalHtml(): string {
  return String.raw`<!DOCTYPE html>
<html>
  <head>
    <meta charset="utf-8" />
    <meta
      name="viewport"
      content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no"
    />
    <link
      rel="stylesheet"
      href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css"
    />
    <style>
      html, body {
        margin: 0;
        padding: 0;
        background-color: #000000;
        height: 100%;
        overflow: hidden;
        font-family: monospace;
        -webkit-user-select: none;
        user-select: none;
      }
      #app {
        position: relative;
        width: 100%;
        height: 100%;
      }
      #terminal {
        position: absolute;
        inset: 0;
      }
      .banner {
        position: absolute;
        left: 50%;
        transform: translateX(-50%);
        padding: 8px 12px;
        border-radius: 12px;
        font-size: 12px;
        color: #ffffff;
        background-color: rgba(15, 118, 255, 0.8);
        pointer-events: none;
        z-index: 5;
        opacity: 0;
        transition: opacity 0.2s ease;
      }
      .banner.visible {
        opacity: 1;
      }
      #status-banner {
        top: 16px;
      }
      #error-banner {
        bottom: 16px;
        background-color: rgba(239, 68, 68, 0.85);
      }
    </style>
  </head>
  <body>
    <div id="app">
      <div id="terminal"></div>
      <div id="status-banner" class="banner"></div>
      <div id="error-banner" class="banner"></div>
    </div>

    <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
    <script>
      (function () {
        const FRAME_HEADER_SIZE = 5;
        const FRAME_TYPE_OUTPUT = 0;
        const STATUS_MESSAGES = {
          'checking-session': 'Verifying session...',
          'session-missing': 'Session not registered with relay.',
          connecting: 'Connecting to relay...',
          disconnected: 'Disconnected from relay.',
          ended: 'Session finished.',
          preparing: 'Preparing terminal...'
        };

        let term = null;
        let fitAddon = null;
        let socket = null;
        let decoder = null;
        let buffer = new Uint8Array();
        let suppressResize = 0;
        let pendingInit = null;
        let pendingResize = null;
        let activeInstanceId = null;
        let lastStatus = 'idle';

        const statusBanner = document.getElementById('status-banner');
        const errorBanner = document.getElementById('error-banner');
        const terminalContainer = document.getElementById('terminal');

        function post(payload) {
          try {
            window.ReactNativeWebView &&
              window.ReactNativeWebView.postMessage(JSON.stringify(payload));
          } catch (err) {
            console.log('postMessage error', err);
          }
        }

        function setStatus(nextStatus, messageOverride) {
          lastStatus = nextStatus;
          post({ type: 'status', status: nextStatus });

          const bannerMessage =
            messageOverride || STATUS_MESSAGES[nextStatus] || '';
          if (bannerMessage) {
            statusBanner.textContent = bannerMessage;
            statusBanner.classList.add('visible');
          } else {
            statusBanner.classList.remove('visible');
          }
        }

        function setError(message, statusOverride) {
          if (message) {
            errorBanner.textContent = message;
            errorBanner.classList.add('visible');
          } else {
            errorBanner.textContent = '';
            errorBanner.classList.remove('visible');
          }
          post({ type: 'error', message: message || null, status: statusOverride });
        }

        function resetTerminal() {
          if (socket) {
            try {
              socket.close();
            } catch (err) {}
            socket = null;
          }
          buffer = new Uint8Array();
          decoder = null;

          if (term) {
            try {
              term.reset();
              term.clear();
            } catch (err) {}
          }

          setStatus('idle');
          setError(null);
          activeInstanceId = null;
        }

        function createTerminal() {
          if (term) {
            return;
          }

          term = new window.Terminal({
            fontFamily: '"Berkeley Mono", "Fira Code", monospace',
            allowProposedApi: true,
            convertEol: false,
            theme: {
              background: '#000000',
              foreground: '#dddddd'
            }
          });

          fitAddon = new window.FitAddon.FitAddon();
          term.loadAddon(fitAddon);

          term.onData(function (data) {
            sendInput(data);
          });

          term.onResize(function (size) {
            pendingResize = { cols: size.cols, rows: size.rows };
            if (suppressResize > 0) {
              return;
            }
            sendResize(size.cols, size.rows);
          });

          const clearHandler = term.parser.registerCsiHandler({ final: 'J' }, function (params) {
            const code = params.length === 0 ? 0 : params[0];
            if (code === 2) {
              term.clear();
              term.scrollToTop();
            }
            return false;
          });

          term.open(terminalContainer);

          window.addEventListener('resize', function () {
            runFit();
            const measured = getCurrentSize();
            if (measured) {
              pendingResize = measured;
              sendResize(measured.cols, measured.rows);
            }
          });

          window.addEventListener('unload', function () {
            try {
              clearHandler.dispose();
            } catch (err) {}
          });

          setTimeout(runFit, 50);
        }

        function runFit() {
          if (!fitAddon || !term) {
            return;
          }
          if (!term.element || term.element.offsetWidth === 0) {
            return;
          }
          suppressResize += 1;
          try {
            fitAddon.fit();
          } catch (err) {
          } finally {
            suppressResize = Math.max(0, suppressResize - 1);
          }
        }

        function getCurrentSize() {
          if (!term) {
            return null;
          }
          const cols = Number(term.cols);
          const rows = Number(term.rows);
          if (!Number.isFinite(cols) || !Number.isFinite(rows)) {
            return null;
          }
          return { cols, rows };
        }

        function sendInput(data) {
          if (!socket || socket.readyState !== WebSocket.OPEN) {
            return;
          }

          const payload = { type: 'input', data: data };
          const size = getCurrentSize();
          if (size) {
            payload.cols = size.cols;
            payload.rows = size.rows;
          }

          try {
            socket.send(JSON.stringify(payload));
          } catch (err) {}
        }

        function sendResize(cols, rows) {
          if (!socket || socket.readyState !== WebSocket.OPEN) {
            return;
          }

          if (!Number.isFinite(cols)) {
            return;
          }

          const safeCols = Math.max(1, Math.trunc(cols));

          try {
            // Only send width - height stays constant
            socket.send(
              JSON.stringify({
                type: 'resize_request',
                cols: safeCols
              })
            );
          } catch (err) {}
        }

        function appendBuffer(chunk) {
          if (!chunk || !chunk.length) {
            return;
          }
          const combined = new Uint8Array(buffer.length + chunk.length);
          combined.set(buffer, 0);
          combined.set(chunk, buffer.length);
          buffer = combined;
        }

        function shiftBuffer(length) {
          buffer = buffer.slice(length);
        }

        function processFrames() {
          if (!term) {
            buffer = new Uint8Array();
            return;
          }

          while (buffer.length >= FRAME_HEADER_SIZE) {
            const view = new DataView(
              buffer.buffer,
              buffer.byteOffset,
              buffer.byteLength
            );

            const frameType = view.getUint8(0);
            const frameLength = view.getUint32(1);
            const total = FRAME_HEADER_SIZE + frameLength;

            if (buffer.length < total) {
              return;
            }

            const payload = buffer.slice(FRAME_HEADER_SIZE, total);
            shiftBuffer(total);

            if (frameType === FRAME_TYPE_OUTPUT) {
              if (!decoder) {
                decoder = new TextDecoder('utf-8');
              }
              const text = decoder.decode(payload, { stream: true });
              if (text) {
                term.write(text);
                // Auto-scroll to bottom when new data arrives
                setTimeout(function () {
                  term.scrollToBottom();
                }, 0);
              }
            }
          }
        }

        function applyResize(cols, rows) {
          if (!term) {
            return;
          }

          const normalizedCols = Number(cols);
          const normalizedRows = Number(rows);
          if (!Number.isFinite(normalizedCols) || !Number.isFinite(normalizedRows)) {
            return;
          }

          const safeCols = Math.max(1, Math.trunc(normalizedCols));
          const safeRows = Math.max(1, Math.trunc(normalizedRows));

          pendingResize = { cols: safeCols, rows: safeRows };

          if (term.cols === safeCols && term.rows === safeRows) {
            return;
          }

          suppressResize += 1;
          try {
            term.resize(safeCols, safeRows);
          } finally {
            suppressResize = Math.max(0, suppressResize - 1);
          }
        }

        function disposeSocket() {
          if (socket) {
            try {
              socket.onopen = null;
              socket.onclose = null;
              socket.onerror = null;
              socket.onmessage = null;
              socket.close();
            } catch (err) {}
            socket = null;
          }
        }

        function flushDecoder() {
          if (!decoder || !term) {
            return;
          }
          try {
            const remainder = decoder.decode();
            if (remainder) {
              term.write(remainder);
            }
          } catch (err) {}
        }

        function handleJsonMessage(message) {
          let payload = null;
          try {
            payload = JSON.parse(message);
          } catch (err) {
            return;
          }

          const kind = payload && payload.type;
          if (kind === 'resize') {
            applyResize(payload.cols, payload.rows);
          } else if (kind === 'error') {
            setStatus('error');
            setError(payload.message || 'Relay reported an error');
          } else if (kind === 'session_ended') {
            setStatus('ended');
            setError('Session has finished running.');
          }
        }

        function connectSocket(payload) {
          const { relayConfig, accessToken, instanceId, supabaseSubprotocolPrefix } = payload;

          disposeSocket();
          buffer = new Uint8Array();
          decoder = new TextDecoder('utf-8');

          setStatus('connecting');
          setError(null);

          let wsUrl = relayConfig.baseWsUrl;
          if (!wsUrl.endsWith('/terminal')) {
            wsUrl = wsUrl + '/terminal';
          }

          try {
            socket = new WebSocket(wsUrl, supabaseSubprotocolPrefix + accessToken);
          } catch (err) {
            setStatus('error');
            setError('Failed to open relay WebSocket.');
            socket = null;
            return;
          }

          socket.binaryType = 'arraybuffer';

          socket.onopen = function () {
            setStatus('connected');
            try {
              socket.send(
                JSON.stringify({
                  type: 'join_session',
                  session_id: instanceId
                })
              );
            } catch (err) {}

            const initial = pendingResize || getCurrentSize();
            if (initial) {
              sendResize(initial.cols, initial.rows);
            }
          };

          socket.onclose = function (event) {
            if (lastStatus !== 'ended') {
              setStatus('disconnected');
            }
            flushDecoder();
            if (event && event.code === 1008) {
              setError('Relay rejected authentication credentials.');
            }
            socket = null;
          };

          socket.onerror = function () {
            setStatus('error');
            setError('WebSocket error while streaming session.');
          };

          socket.onmessage = function (event) {
            const data = event && event.data;

            if (typeof data === 'string') {
              handleJsonMessage(data);
              return;
            }

            if (data instanceof ArrayBuffer) {
              appendBuffer(new Uint8Array(data));
              processFrames();
              return;
            }

            if (data instanceof Blob) {
              data
                .arrayBuffer()
                .then(function (bufferData) {
                  appendBuffer(new Uint8Array(bufferData));
                  processFrames();
                })
                .catch(function () {
                  setStatus('error');
                  setError('Failed to read binary relay frame.');
                });
              return;
            }

            try {
              const text = String(data);
              appendBuffer(new TextEncoder().encode(text));
              processFrames();
            } catch (err) {}
          };
        }

        function ensureSession(payload) {
          const { relayConfig, accessToken, instanceId } = payload;

          setStatus('checking-session');
          setError(null);

          let httpUrl = relayConfig.baseHttpUrl;
          if (!httpUrl.endsWith('/api/v1/sessions')) {
            httpUrl = httpUrl + '/api/v1/sessions';
          }

          return fetch(httpUrl, {
            method: 'GET',
            headers: {
              Authorization: 'Bearer ' + accessToken
            }
          })
            .then(function (response) {
              if (!response.ok) {
                throw new Error('Relay returned status ' + response.status);
              }
              return response.json();
            })
            .then(function (data) {
              const sessions = Array.isArray(data && data.sessions)
                ? data.sessions
                : [];
              const matched = sessions.some(function (session) {
                return session && session.id === instanceId;
              });
              if (!matched) {
                setStatus('session-missing');
                setError('Session is not currently registered with the relay.');
                return false;
              }
              return true;
            })
            .catch(function (err) {
              setStatus('error');
              setError(err && err.message
                ? err.message
                : 'Failed to reach relay.');
              return false;
            });
        }

        async function startSession(payload) {
          if (!term) {
            pendingInit = payload;
            return;
          }

          if (!payload || !payload.instanceId) {
            setStatus('error');
            setError('Missing session parameters.');
            return;
          }

          if (activeInstanceId && activeInstanceId !== payload.instanceId) {
            resetTerminal();
          }

          activeInstanceId = payload.instanceId;

          const ok = await ensureSession(payload);
          if (!ok) {
            return;
          }

          connectSocket(payload);
        }

        function handleInboundMessage(event) {
          const raw = event && event.data;
          if (typeof raw !== 'string') {
            return;
          }

          let payload = null;
          try {
            payload = JSON.parse(raw);
          } catch (err) {
            return;
          }

          if (!payload || !payload.type) {
            return;
          }

          if (payload.type === 'init') {
            startSession(payload.payload);
          } else if (payload.type === 'reset') {
            resetTerminal();
          } else if (payload.type === 'keySequence') {
            sendInput(payload.data);
          } else if (payload.type === 'blur') {
            if (term && term.textarea) {
              term.textarea.blur();
            }
          }
        }

        document.addEventListener('message', handleInboundMessage);
        window.addEventListener('message', handleInboundMessage);

        window.addEventListener('load', function () {
          createTerminal();
          runFit();
          post({ type: 'ready' });
          if (pendingInit) {
            const next = pendingInit;
            pendingInit = null;
            startSession(next);
          }

          // Scroll to bottom on load
          setTimeout(function () {
            if (term) {
              term.scrollToBottom();
            }
          }, 100);
        });
      })();
    </script>
  </body>
</html>`;
}

function statusToMessage(status: TerminalStatus, error: string | null): string | null {
  if (status === 'connected') {
    return null;
  }
  if (status === 'error' || status === 'session-missing') {
    return error || 'Unable to load terminal.';
  }
  switch (status) {
    case 'idle':
    case 'preparing':
      return 'Preparing terminal...';
    case 'checking-session':
      return 'Verifying session...';
    case 'connecting':
      return 'Connecting to relay...';
    case 'disconnected':
      return 'Disconnected from live session.';
    case 'ended':
      return 'Session has finished running.';
    default:
      return null;
  }
}

interface TerminalMobileTerminalProps {
  instanceId: string;
}

export interface TerminalMobileTerminalRef {
  sendKeySequence: (sequence: string) => void;
  blurTerminal: () => void;
}

export const TerminalMobileTerminal = React.forwardRef<
  TerminalMobileTerminalRef,
  TerminalMobileTerminalProps
>(({ instanceId }, ref) => {
  const webViewRef = useRef<WebViewType | null>(null);
  const activeInstanceRef = useRef<string | null>(null);
  const isMountedRef = useRef(true);
  const [webViewReady, setWebViewReady] = useState(false);
  const [status, setStatus] = useState<TerminalStatus>('idle');
  const [error, setErrorState] = useState<string | null>(null);
  const [initializing, setInitializing] = useState(false);

  const relayConfig = useMemo(() => buildRelayConfig(), []);
  const terminalHtml = useMemo(() => buildTerminalHtml(), []);

  const sendKeySequence = useCallback((sequence: string) => {
    if (!webViewReady) {
      return;
    }
    try {
      webViewRef.current?.postMessage(
        JSON.stringify({ type: 'keySequence', data: sequence })
      );
    } catch (err) {
      reportError(err, {
        context: 'Failed to send key sequence',
        extras: { instanceId, sequence },
        tags: { feature: 'mobile-terminal-keys' },
      });
    }
  }, [instanceId, webViewReady]);

  const blurTerminal = useCallback(() => {
    if (!webViewReady) {
      return;
    }
    try {
      webViewRef.current?.postMessage(
        JSON.stringify({ type: 'blur' })
      );
    } catch (err) {
      reportError(err, {
        context: 'Failed to blur terminal',
        extras: { instanceId },
        tags: { feature: 'mobile-terminal-keys' },
      });
    }
  }, [instanceId, webViewReady]);

  React.useImperativeHandle(ref, () => ({
    sendKeySequence,
    blurTerminal,
  }), [sendKeySequence, blurTerminal]);

  useEffect(() => {
    return () => {
      isMountedRef.current = false;
      try {
        webViewRef.current?.postMessage(
          JSON.stringify({ type: 'reset' })
        );
      } catch (err) {}
    };
  }, []);

  const transmitInitMessage = useCallback(async () => {
    if (!webViewReady || !instanceId) {
      return;
    }

    setInitializing(true);
    setStatus('preparing');
    setErrorState(null);

    try {
      const { data } = await supabase.auth.getSession();
      const token = data.session?.access_token;
      if (!token) {
        setStatus('error');
        setErrorState('Sign in to view the live terminal stream.');
        return;
      }

      const payload: InitMessagePayload = {
        instanceId,
        accessToken: token,
        relayConfig,
        supabaseSubprotocolPrefix: SUPABASE_SUBPROTOCOL_PREFIX,
      };

      if (activeInstanceRef.current && activeInstanceRef.current !== instanceId) {
        try {
          webViewRef.current?.postMessage(
            JSON.stringify({ type: 'reset' })
          );
        } catch (err) {}
      }

      activeInstanceRef.current = instanceId;

      webViewRef.current?.postMessage(
        JSON.stringify({ type: 'init', payload })
      );
    } catch (err) {
      reportError(err, {
        context: 'Failed to initialise terminal',
        extras: { instanceId },
        tags: { feature: 'mobile-terminal' },
      });
      if (!isMountedRef.current) {
        return;
      }
      setStatus('error');
      setErrorState(err instanceof Error ? err.message : 'Unable to start terminal session.');
    } finally {
      if (isMountedRef.current) {
        setInitializing(false);
      }
    }
  }, [instanceId, relayConfig, webViewReady]);

  useEffect(() => {
    if (webViewReady) {
      transmitInitMessage();
    }
  }, [webViewReady, transmitInitMessage]);

  const handleWebViewMessage = useCallback(
    (event: WebViewMessageEvent) => {
      if (!event?.nativeEvent?.data) {
        return;
      }
      let payload: WebViewMessage | null = null;
      try {
        payload = JSON.parse(event.nativeEvent.data);
      } catch (err) {
        return;
      }

      if (!payload || !payload.type) {
        return;
      }

      switch (payload.type) {
        case 'ready':
          setWebViewReady(true);
          break;
        case 'status':
          if (payload.status) {
            setStatus(payload.status);
            if (payload.status === 'connected') {
              setErrorState(null);
            }
          }
          break;
        case 'error':
          if (typeof payload.message === 'string') {
            setErrorState(payload.message);
          }
          if (payload.status) {
            setStatus(payload.status);
          } else if (payload.message) {
            setStatus('error');
          }
          break;
        default:
          break;
      }
    },
    []
  );

  const overlayMessage = statusToMessage(status, error);
  const showOverlay = Boolean(overlayMessage) || initializing;

  return (
    <View style={styles.container}>
      <WebView
        ref={webViewRef}
        originWhitelist={["*"]}
        source={{ html: terminalHtml }}
        onMessage={handleWebViewMessage}
        javaScriptEnabled
        domStorageEnabled
        hideKeyboardAccessoryView
        keyboardDisplayRequiresUserAction={false}
        allowsInlineMediaPlayback
        allowFileAccess={true}
        allowUniversalAccessFromFileURLs={true}
        mixedContentMode="always"
        style={styles.webview}
        autoManageStatusBarEnabled={false}
      />
      {showOverlay && (
        <View style={styles.overlay} pointerEvents="none">
          {initializing ? (
            <ActivityIndicator size="small" color={theme.colors.white} />
          ) : null}
          {overlayMessage ? (
            <Text
              style={[
                styles.overlayText,
                initializing ? styles.overlayTextWithIndicator : null,
              ]}
            >
              {overlayMessage}
            </Text>
          ) : null}
        </View>
      )}
    </View>
  );
});

TerminalMobileTerminal.displayName = 'TerminalMobileTerminal';

const styles = StyleSheet.create({
  container: {
    flex: 1,
    borderRadius: theme.borderRadius.lg,
    overflow: 'hidden',
    borderWidth: 1,
    borderColor: '#1f2937',
    backgroundColor: '#000',
  },
  webview: {
    flex: 1,
    backgroundColor: '#000',
  },
  overlay: {
    position: 'absolute',
    top: 12,
    left: 12,
    right: 12,
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
    borderRadius: theme.borderRadius.lg,
    backgroundColor: 'rgba(15, 118, 255, 0.16)',
    alignItems: 'center',
  },
  overlayText: {
    color: theme.colors.white,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.semibold,
    textAlign: 'center',
  },
  overlayTextWithIndicator: {
    marginTop: theme.spacing.xs,
  },
});
