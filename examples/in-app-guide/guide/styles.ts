export function guideCss(accent: string) {
  return `
:host { all: initial; }
* { box-sizing: border-box; }
.g-root, .g-layer {
  --g-accent: ${accent};
  --g-accent-soft: color-mix(in srgb, ${accent} 14%, transparent);
  --g-bg: #ffffff;
  --g-fg: #0f172a;
  --g-muted: #64748b;
  --g-border: #e2e8f0;
  --g-subtle: #f8fafc;
  font-family: var(--g-font, ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif);
  font-size: 14px;
  line-height: 1.45;
  color: var(--g-fg);
}
@media (prefers-color-scheme: dark) {
  .g-root[data-theme="auto"] { --g-bg: #0f172a; --g-fg: #e2e8f0; --g-muted: #94a3b8; --g-border: #1e293b; --g-subtle: #111c33; }
}

/* overlay */
.g-cursor, .g-ring, .g-caption { position: fixed; top: 0; left: 0; z-index: 2147483600; opacity: 0; transition: opacity .18s ease; }
.g-visible { opacity: 1; }
.g-cursor { pointer-events: none; filter: drop-shadow(0 2px 3px rgba(15, 23, 42, .35)); will-change: transform; }
.g-cursor.g-animate { transition: opacity .18s ease, transform .6s cubic-bezier(.22,.8,.3,1); }
.g-cursor svg { display: block; transition: transform .12s ease; transform-origin: 3px 2px; }
.g-cursor.g-press svg { transform: scale(.82); }
.g-ring { pointer-events: none; border: 2px solid var(--g-accent); border-radius: 9px; box-shadow: 0 0 0 4px var(--g-accent-soft); animation: g-pulse 1.6s ease-in-out infinite; }
@keyframes g-pulse { 50% { box-shadow: 0 0 0 7px var(--g-accent-soft); } }
.g-caption { pointer-events: auto; max-width: 300px; background: #0f172a; color: #f8fafc; border-radius: 10px; padding: 10px 12px; box-shadow: 0 10px 30px rgba(15, 23, 42, .25); font-size: 13px; }
.g-caption-text { margin: 0; }
.g-caption-actions { display: flex; gap: 6px; margin-top: 8px; }
.g-caption .g-btn { background: transparent; color: #cbd5e1; border: 1px solid #334155; }
.g-caption .g-btn-primary { background: var(--g-accent); color: white; border-color: var(--g-accent); }

/* buttons */
.g-btn { font: inherit; font-size: 12px; font-weight: 500; padding: 5px 10px; border-radius: 7px; border: 1px solid var(--g-border); background: var(--g-bg); color: var(--g-fg); cursor: pointer; }
.g-btn:hover { filter: brightness(.97); }
.g-btn-primary { background: var(--g-accent); border-color: var(--g-accent); color: white; }
.g-btn:focus-visible, .g-icon:focus-visible, .g-launcher:focus-visible, .g-seg button:focus-visible { outline: 2px solid var(--g-accent); outline-offset: 2px; }

/* launcher */
.g-root[data-placement="bottom-right"] :is(.g-launcher, .g-panel, .g-pill) { right: var(--g-side, 20px); }
.g-root[data-placement="bottom-left"] :is(.g-launcher, .g-panel, .g-pill) { left: var(--g-side, 20px); }
.g-pill { position: fixed; bottom: calc(var(--g-bottom, 20px) + 56px); z-index: 2147483500; display: flex; align-items: center; gap: 10px; width: 300px; max-width: calc(100vw - 32px); padding: 8px 8px 8px 14px; background: var(--g-bg); border: 1px solid var(--g-border); border-radius: 999px; box-shadow: 0 10px 30px rgba(15, 23, 42, .14); font-size: 12.5px; color: var(--g-muted); }
.g-launcher { position: fixed; bottom: var(--g-bottom, 20px); z-index: 2147483500; display: flex; align-items: center; gap: 8px; padding: 10px 16px 10px 13px; border-radius: 999px; border: none; background: var(--g-accent); color: white; font: inherit; font-weight: 600; font-size: 14px; cursor: pointer; box-shadow: 0 8px 24px color-mix(in srgb, var(--g-accent) 40%, transparent); }
.g-launcher svg { width: 18px; height: 18px; }

/* panel */
.g-panel { position: fixed; bottom: calc(var(--g-bottom, 20px) + 56px); z-index: 2147483500; width: min(390px, calc(100vw - 32px)); height: min(600px, calc(100vh - 100px)); display: flex; flex-direction: column; background: var(--g-bg); border: 1px solid var(--g-border); border-radius: 16px; box-shadow: 0 24px 60px rgba(15, 23, 42, .18); overflow: hidden; }
.g-head { display: flex; align-items: center; gap: 8px; padding: 12px 12px 10px 16px; border-bottom: 1px solid var(--g-border); }
.g-title { font-weight: 600; font-size: 15px; }
.g-sub { font-size: 11.5px; color: var(--g-muted); }
.g-grow { flex: 1; min-width: 0; }
.g-icon { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 8px; border: none; background: transparent; color: var(--g-muted); cursor: pointer; }
.g-icon:hover { background: var(--g-subtle); color: var(--g-fg); }
.g-icon svg { width: 16px; height: 16px; }
.g-modebar { padding: 8px 12px; border-bottom: 1px solid var(--g-border); display: flex; align-items: center; gap: 10px; }
.g-seg { display: inline-flex; padding: 2px; background: var(--g-subtle); border: 1px solid var(--g-border); border-radius: 9px; }
.g-seg button { font: inherit; font-size: 12.5px; font-weight: 500; border: none; background: transparent; color: var(--g-muted); padding: 4px 10px; border-radius: 7px; cursor: pointer; }
.g-seg button[aria-pressed="true"] { background: var(--g-bg); color: var(--g-fg); box-shadow: 0 1px 2px rgba(15, 23, 42, .12); }
.g-modehint { font-size: 11.5px; color: var(--g-muted); }

.g-body { flex: 1; overflow-y: auto; padding: 14px 14px 6px; display: flex; flex-direction: column; gap: 10px; }
.g-msg { max-width: 88%; padding: 8px 11px; border-radius: 12px; white-space: pre-wrap; overflow-wrap: anywhere; }
.g-msg p { margin: 0; }
.g-msg p + p { margin-top: 6px; }
.g-msg a { color: var(--g-accent); }
.g-user { align-self: flex-end; background: var(--g-accent); color: white; border-bottom-right-radius: 4px; }
.g-user a { color: white; }
.g-assistant { align-self: flex-start; background: var(--g-subtle); border: 1px solid var(--g-border); border-bottom-left-radius: 4px; }
.g-notice { align-self: stretch; max-width: none; font-size: 12.5px; color: #92400e; background: #fffbeb; border: 1px solid #fde68a; }
.g-preview { opacity: .75; }
.g-empty { color: var(--g-muted); font-size: 13px; }
.g-empty strong { color: var(--g-fg); }
.g-chips { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }
.g-chip { text-align: left; font: inherit; font-size: 13px; padding: 8px 10px; border-radius: 9px; border: 1px solid var(--g-border); background: var(--g-bg); color: var(--g-fg); cursor: pointer; }
.g-chip:hover { border-color: var(--g-accent); }

.g-history { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 4px; }
.g-history-title { font-size: 11.5px; font-weight: 600; color: var(--g-muted); padding: 2px 4px 6px; }
.g-history-item { display: flex; flex-direction: column; gap: 2px; text-align: left; font: inherit; padding: 9px 10px; border-radius: 10px; border: 1px solid transparent; background: transparent; color: var(--g-fg); cursor: pointer; }
.g-history-item:hover { background: var(--g-subtle); border-color: var(--g-border); }
.g-history-item[aria-current="true"] { border-color: var(--g-accent); background: var(--g-accent-soft); }
.g-history-name { font-size: 13.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.g-history-when { font-size: 11.5px; color: var(--g-muted); }
.g-icon[aria-pressed="true"] { background: var(--g-subtle); color: var(--g-fg); }
[hidden] { display: none !important; }
.g-status { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--g-muted); padding: 2px 2px; }
.g-dots { display: inline-flex; gap: 3px; }
.g-dots i { width: 5px; height: 5px; border-radius: 50%; background: var(--g-accent); animation: g-bounce 1s infinite ease-in-out; }
.g-dots i:nth-child(2) { animation-delay: .15s; }
.g-dots i:nth-child(3) { animation-delay: .3s; }
@keyframes g-bounce { 0%, 80%, 100% { opacity: .25; } 40% { opacity: 1; } }

.g-approval { border: 1px solid var(--g-accent); background: var(--g-accent-soft); border-radius: 12px; padding: 10px 12px; }
.g-approval h4 { margin: 0 0 4px; font-size: 13px; }
.g-approval p { margin: 0 0 8px; font-size: 13px; }
.g-approval .g-row { display: flex; gap: 6px; }

.g-compose { display: flex; gap: 8px; align-items: flex-end; padding: 10px 12px 12px; border-top: 1px solid var(--g-border); }
.g-compose textarea { flex: 1; resize: none; font: inherit; font-size: 14px; color: var(--g-fg); background: var(--g-bg); border: 1px solid var(--g-border); border-radius: 10px; padding: 8px 10px; max-height: 120px; min-height: 38px; outline: none; }
.g-compose textarea:focus { border-color: var(--g-accent); box-shadow: 0 0 0 3px var(--g-accent-soft); }
.g-send { width: 38px; height: 38px; border-radius: 10px; border: none; background: var(--g-accent); color: white; display: grid; place-items: center; cursor: pointer; }
.g-send:disabled { opacity: .45; cursor: default; }
.g-send svg { width: 16px; height: 16px; }
.g-foot { text-align: center; font-size: 10.5px; color: var(--g-muted); padding: 0 0 8px; }
`
}
