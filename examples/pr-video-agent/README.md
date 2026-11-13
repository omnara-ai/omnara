# PR Video Agent

An agent that shows reviewers a pull request working. When a pull request
changes something you can see in the browser, it checks out the branch, runs
your app on its own machine, clicks through the change in Chrome, and posts
a short recording on the pull request:

> **acme-pr-video** commented\
> Adding a high-priority task with the new priority picker: it lands first
> in the list with a red **High** badge.
>
> *(a 20-second recording plays here)*
>
> Recorded at 3f9c2ab.

It stays silent on pull requests with nothing to see: documentation, tests,
CI, dependency bumps, and refactors. It looks at each new push and records
again when the push changes what a reviewer would see, and it doesn't
record the same commit twice: asked again, it points to the recording it
already posted. Mention it to ask for a different path through the app. It
works on pull requests from branches in your repository, not from forks.

Omnara runs it on its own repository. To see real recordings, including a
second one after a later push, look at
[omnara-ai/omnara#845](https://github.com/omnara-ai/omnara/pull/845).

You choose when it runs: on every new pull request, only when mentioned, or
both (the default).

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with a machine
  pool; `default-pool` works as is
- A GitHub account or organization where you can register a GitHub App and
  install it on your repository
- A web app the agent can start from a fresh clone on a Linux machine, with
  whatever it needs running there too

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/pr-video-agent/SKILL.md and follow it to deploy the PR video agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. It works out
how to run your app, or asks you. Run it again any time to change the agent;
it updates it in place.

## GIF or video

GitHub plays a video in a comment only when the file was uploaded to GitHub
itself, and it accepts those uploads from a person's token, not from a
GitHub App. So there are two ways to post the recording:

| | GIF (default) | Video |
| --- | --- | --- |
| Reviewers see | An animated GIF in the comment, linked to the MP4 | A video player in the comment |
| Posted by | The GitHub App | The person whose token it is |
| Needs | The App allowed to push ("Let agents push code") | A fine-grained personal access token, stored as an Omnara secret |
| Stored | In your repository, on a hidden ref | In GitHub's attachment storage |

**GIF.** The agent commits `video.gif` and `video.mp4` to the ref
`refs/uploads/pr-<number>` in your repository and embeds the GIF from there,
so whoever can see the repository can see the recording: everyone, if the
repository is public. On a private repository the image loads through the
viewer's own GitHub session, so it shows on the pull request page for
people signed in with access, and not in email notifications. That ref
isn't a branch: it doesn't show in the branch list, a normal clone doesn't
download it, and merging the pull request never includes it. The files do
count toward the repository's size, and deleting the ref breaks the images
in old comments.

**Video.** The agent posts the comment with the GitHub CLI's `--attach`
(version 2.99 or later, on GitHub.com only), using a token with **Contents**
and **Pull requests** write access to the one repository. The App doesn't
need to push.

Either way, a token that can write to the repository is on the machine that
runs the pull request's code, as it would be in CI. Install the App on the
one repository only, and scope a personal token to it as well. For the same
reason it records only pull requests from branches in the repository
itself. It refuses a pull request from a fork whoever asks, because a fork's
author can push new code at any time and that code would run next to the
token. Recording forks safely would need the app to run without access to
the credentials that publish the recording, which this example doesn't do.

## How it works

[agent.yaml](agent.yaml) is the whole agent. Omnara's
[GitHub integration](https://docs.omnara.com/integrations/github) receives
your App's webhooks, and its launcher starts one agent per pull request, with
its own machine. The launcher also gives that agent the pull request tools
and Git credentials minted from the App's installation, so no token appears
in the config or the event log. Only people with write access can start it,
by opening a pull request or mentioning the App.

The agent first reads the pull request and decides whether there is anything
to record. If there is, it clones the repository, checks out the pull
request's head, and starts the app with the notes you gave it. It rehearses
in Chrome with [agent-browser](https://github.com/vercel-labs/agent-browser),
then writes the path as a script and records that in one run, so the video
has no pauses while the model thinks. Three helper scripts written by the
machine's startup script do the rest: `video-setup` installs Chrome and
ffmpeg, `video-convert` converts the take to MP4 and GIF and makes a sheet of
frames the agent checks before posting, and `video-post` publishes it one of
the two ways above. All of it is plain text in `agent.yaml` that you can
read and edit.

A first recording takes several minutes, most of it installing Chrome on a
new machine. To cut that, use a custom pool whose image already has Chrome,
ffmpeg, and `agent-browser`.
