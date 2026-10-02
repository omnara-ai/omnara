# SRE Agent

A production investigator. Ask it about a symptom ("5xx spike on checkout"),
paste an alert, or ask why something is slow. It works through AWS, plus
Grafana and your source code if you connect them, the way an on-call engineer
would, and replies with the most likely cause and the evidence behind it:

> **Most likely cause:** the 14:02 UTC deploy of `checkout-api` (task definition revision 212) set the database pool to 5 connections.\
> • ALB 5xx on `checkout-api` went from under 0.1% to 6% at 14:04, three minutes after revision 212 reached steady state.\
> • 1,840 log lines since 14:04 read `timeout acquiring connection from pool`; none in the hour before.\
> • RDS `checkout-db` connections dropped from about 60 to 15, while CPU stayed under 30%.\
> • [config/db.go#L41](https://github.com/acme/checkout/blob/main/config/db.go#L41) changed `MaxOpenConns` from 25 to 5 in the commit that deploy shipped.\
> Rolling back to revision 211 should restore checkout; the pool size needs fixing before redeploying.

It never changes production: its AWS credentials are read-only, and with
Grafana connected, the only thing it can write is dashboards, which it builds
when a result is too much for one message. Reply to it wherever you use it (your own app,
Slack, or the Omnara console) to dig further.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com))
- An AWS account you can create an IAM user in. The deploy sets one up with
  read-only policies.
- Optional: Grafana (Cloud or self-hosted), for dashboards, Prometheus, Loki,
  and other datasources.
- Optional: a GitHub repository for the agent to read, so it can cite the
  code behind an error.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/sre-agent/SKILL.md and follow it to deploy the SRE agent.
```

Your coding agent follows [SKILL.md](SKILL.md): it logs you in to Omnara,
asks about your system, sets up read-only AWS credentials (you put the keys in
a `.env` file, or it creates the IAM user for you), connects Grafana and your
repository if you want them, creates the agent from [agent.yaml](agent.yaml),
and runs a first health check. Then it helps you put the agent in your own app (existing or new) or Slack, and optionally runs a daily health check. Ask it to change
anything along the way, like the instruction or the model.

Prefer to do it yourself? SKILL.md is plain steps with the exact `npx omnara`
commands. Run this again any time to add a data source; it updates the agent
in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. It's modeled on the agent Omnara
uses to investigate its own production.

- **AWS:** the [AWS MCP Server](https://docs.aws.amazon.com/aws-mcp/latest/userguide/what-is-mcp-server.html).
  Omnara signs each call with the stored credentials, so the IAM policy
  (`ViewOnlyAccess` and `CloudWatchReadOnlyAccess`) is the boundary on what
  the agent can do. The tool that creates S3 upload links is left off.
- **Grafana:** Grafana's MCP server, with dashboard, Prometheus, Loki,
  CloudWatch, SQL, alert-rule, and incident read tools enabled. The only
  write tool is `update_dashboard`, which creates and updates dashboards.
  Point SQL datasources at read replicas.
- **Source code:** the repository is cloned on a machine from your project's
  pool when the agent starts; a private-repository token is injected as an
  environment variable and never appears in the config or the event log.

How it diagnoses, how it reports, and what it may never touch are plain
instruction text you can read and edit. To add another data source, such as
Cloudflare or your error tracker, add its MCP server to the `mcp` block the
same way.
