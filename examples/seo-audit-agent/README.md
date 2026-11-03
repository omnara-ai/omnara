# SEO Audit Agent

An agent that audits a web page for an SEO agency. Give it a client's URL
and it checks the page for classic search (SEO), answer engines (AEO), and
AI search citations (GEO), compares it with the pages that compete for its
query, and delivers a report as a PDF plus a markdown copy:

> **SEO audit: Acme Pricing**, for "invoice software for small business"\
> **SEO: B.** Indexable and fast on desktop, but the mobile hero image delays the main content by 2.1 s.\
> **AEO: C.** The page never says what Acme costs in a sentence; the prices live in a JavaScript-rendered table.\
> **GEO: D.** The firewall answers OAI-SearchBot and PerplexityBot with 403, so ChatGPT search and Perplexity likely can't read the page; none of 10 buyer questions surfaced it, and two competitors appeared in 7.\
> **Top fix:** allow AI search crawlers in the firewall rule; the robots.txt already permits them.

Each fix comes ready to paste: a rewritten title and meta description, a
JSON-LD block with the page's real details, robots.txt lines, and an
answer-first opening paragraph. Every finding is marked measured or best
practice, and the report lists what it couldn't check.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with a machine
  pool; `default-pool` works as is
- Optional: a Google API key for real-user speed data, and an OpenAI API key
  to check which sites OpenAI's web search cites. Without them, the agent uses
  lab speed numbers and a simulated AI search check.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/seo-audit-agent/SKILL.md and follow it to deploy the SEO audit agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to add a key or change the agent; it updates it in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. It runs on a machine from your
project's pool, where the startup script installs
[Lighthouse](https://github.com/GoogleChrome/lighthouse) and two helpers
built on Playwright's headless Chromium: one renders the page after
JavaScript runs, the other prints the report as a PDF.

- **Without keys**, it fetches the page as a browser and as each AI crawler,
  compares the HTML before and after JavaScript runs, runs Lighthouse for
  mobile and desktop, and checks robots.txt, the sitemap, metadata, headings,
  and structured data. For AI visibility, it writes the questions a buyer
  would ask and runs them through Omnara's web search, an AI search index, so
  the report labels those results simulated.
- **With `GOOGLE_API_KEY`**, it adds real-user Core Web Vitals from the
  Chrome UX Report.
- **With `OPENAI_API_KEY`**, it asks the same questions through OpenAI's web
  search and reports which sites it cites, measured.

Keys are injected as environment variables on the machine and never appear in
the config or the event log. The checks, the grading, and the report layout
are plain instruction text you can read and edit.
