// Paste into Cloudflare > your domain > Rules > Snippets.
// Restrict the snippet rule to your app, public API and webhook hostnames.
// Preview before deploying. This file does not deploy anything.
const MODE = "off"; // "banner", "maintenance", or "off"
const NOTICE = "Scheduled maintenance: [DATE], [START–END TIME, TIME ZONE]. Omnara will be temporarily unavailable.";

const escapeHTML = (text) => text.replace(/[&<>"']/g, (char) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
})[char]);

const banner = `
  <aside id="omnara-maintenance-notice" role="status" style="
    flex:none; box-sizing:border-box; padding:10px 16px;
    background:#fff1c2; color:#422d08; text-align:center;
    font:500 14px/1.5 system-ui,sans-serif;">
    ${escapeHTML(NOTICE)}
  </aside>`;

// Keep the notice outside React's root, and let the app fill the remaining height.
const bannerStyles = `<style>
  body:has(> #omnara-maintenance-notice) {
    display:flex; flex-direction:column;
  }
  body:has(> #omnara-maintenance-notice) > #root {
    flex:1; min-height:0; height:auto;
  }
</style>`;

const maintenancePage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Omnara — Scheduled maintenance</title>
  <style>
    body { margin:0; min-height:100vh; display:grid; place-items:center;
      background:#101114; color:#f5f5f5; font:16px/1.6 system-ui,sans-serif; }
    main { max-width:480px; padding:32px; text-align:center; }
    small { color:#c4c7ce; letter-spacing:.16em; }
    h1 { font-size:30px; line-height:1.2; }
    p { color:#c4c7ce; }
    a { display:inline-block; padding:10px 20px; border-radius:8px;
      background:#f5f5f5; color:#101114; text-decoration:none; }
  </style>
</head>
<body><main>
  <small>OMNARA</small>
  <h1>Scheduled maintenance</h1>
  <p>Omnara is undergoing scheduled maintenance.
    Please try again after the maintenance window.</p>
  <a href="/">Try again</a>
</main></body></html>`;

export default {
  async fetch(request) {
    if (MODE === "maintenance") {
      const browserPage = ["GET", "HEAD"].includes(request.method)
        && (request.headers.get("Accept") || "").includes("text/html");
      const body = browserPage ? maintenancePage
        : JSON.stringify({ error: "maintenance", message: "Omnara is temporarily unavailable. Please retry later." });
      return new Response(request.method === "HEAD" ? null : body, {
        status: 503,
        headers: {
          "Content-Type": browserPage ? "text/html; charset=utf-8" : "application/json",
          "Cache-Control": "no-store",
          "Retry-After": "300",
        },
      });
    }

    const response = await fetch(request);
    if (MODE !== "banner" || request.method !== "GET" || response.status !== 200
        || !(response.headers.get("Content-Type") || "").includes("text/html")) {
      return response;
    }

    const rewritten = new HTMLRewriter()
      .on("head", { element(element) { element.append(bannerStyles, { html: true }); } })
      .on("body > #root", { element(element) { element.before(banner, { html: true }); } })
      .transform(response);
    const result = new Response(rewritten.body, rewritten);
    result.headers.set("Cache-Control", "no-store");
    result.headers.delete("Content-Length");
    result.headers.delete("ETag");
    result.headers.delete("Last-Modified");
    return result;
  },
};
