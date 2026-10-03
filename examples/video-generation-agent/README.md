# Video Generation Agent

An agent that makes short videos from a brief. Describe what you want ("a
15-second 16:9 teaser for our launch, logo at the end") and it builds the
video in code with [Remotion](https://www.remotion.dev), checks its own
frames, and delivers the rendered MP4:

> Here's `launch-teaser.mp4` (15 s, 1920×1080, 30 fps, 6.4 MB).\
> • 0–5 s: the logo drifts in over a starfield and settles in the center.\
> • 5–11 s: three feature lines slide in one at a time, each on screen for 2 seconds.\
> • 11–15 s: the logo and "Available today" hold, then fade to black.\
> You didn't mention music, so there's none; attach a track if you want one.

Reply to it wherever you use it (your own app, Slack, or the Omnara console)
to ask for changes, or attach a logo or music for it to use; it changes only
what you asked for and sends a new version.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com))
- A machine pool in your project; `default-pool` works as is. No API keys
  needed.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/video-generation-agent/SKILL.md and follow it to deploy the video generation agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. It uses Claude
Opus 5.5 by default. Run it again any time to change the agent; it updates it
in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. It writes a Remotion project on
a machine from your pool and renders it there, guided by Remotion's
[remotion-best-practices](https://github.com/remotion-dev/skills) skill.
Before delivering, it uploads stills and contact sheets from the render and
looks at them, fixing clipped text, captions that go by too fast, and visual
glitches. The MP4 arrives as a file in the conversation (up to 10 MiB; it
re-encodes larger renders). The review checklist, the delivery rules, and
when it asks you a question are plain instruction text you can read and edit.
