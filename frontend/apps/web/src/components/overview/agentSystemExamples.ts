import {
  Bot,
  Box,
  FlaskConical,
  GitBranch,
  GitPullRequest,
  LayoutDashboard,
  Logs,
  MessageCircleQuestion,
  MessageSquareText,
  Plug,
  Reply,
  Server,
  Ticket,
  User,
  Users,
} from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'

import { GitHubLogo, SlackLogo } from '@/components/overview/codingAppLogos'

/*
 * Each example is a different way to build on Omnara: an agent per user embedded in
 * your own app, with your backend calling the API for each user, a pull request reviewer driven by GitHub webhooks, and
 * a Slack agent reaching your private tools over MCP. Each is laid out on a small grid,
 * with the main flow along the top row and what hangs off it below. Nodes are listed
 * in the order the flow passes through them, which is how they stack on small screens.
 */

export type Icon = ComponentType<SVGProps<SVGSVGElement>>

export interface Chip {
  icon: Icon
  /** What the chip is. Shown beside the icon when `short`, otherwise only on hover. */
  name: string
  short?: string
}

export interface GroupNode {
  id: string
  col: number
  row: number
  /**
   * Omitted for the Omnara group, which shows the mark instead. A labelled group with
   * one chip is drawn as a single card with the chip's icon and this label.
   */
  label?: string
  icon?: Icon
  chips: Chip[]
}

export interface CallsNode {
  id: string
  col: number
  row: number
  /** Nudges the panel up or down in its cell, to clear a link running past it. */
  dy?: number
  title: string
  calls: string[]
}

export type DiagramNode = GroupNode | CallsNode

export interface DiagramEdge {
  from: string
  to: string
  /** Attach to one chip of a group instead of the group's middle. */
  fromChip?: number
  toChip?: number
  /** Shifts an end up or down, so a card's incoming and outgoing links stay apart. */
  fromOffset?: number
  toOffset?: number
}

export interface Example {
  name: string
  nodes: DiagramNode[]
  edges: DiagramEdge[]
}

export const examples: Example[] = [
  {
    name: 'Dashboard agent',
    nodes: [
      {
        id: 'users',
        col: 0,
        row: 1,
        label: 'Users',
        icon: Users,
        chips: [
          { icon: User, name: 'User A' },
          { icon: User, name: 'User B' },
          { icon: User, name: 'User C' },
        ],
      },
      {
        id: 'app',
        col: 0,
        row: 0,
        label: 'Product dashboard',
        chips: [{ icon: LayoutDashboard, name: 'Product dashboard' }],
      },
      {
        id: 'backend',
        col: 1,
        row: 0,
        label: 'Your backend',
        chips: [{ icon: Server, name: 'Your backend, calling Omnara for each user' }],
      },
      {
        id: 'api',
        col: 2,
        row: 0,
        title: 'Omnara API',
        calls: ['POST /agents', 'POST /agents/{id}/inputs'],
      },
      {
        id: 'agents',
        col: 3,
        row: 0,
        chips: [
          { icon: Bot, name: 'Agent for user A' },
          { icon: Bot, name: 'Agent for user B' },
          { icon: Bot, name: 'Agent for user C' },
        ],
      },
      {
        id: 'sandboxes',
        col: 3,
        row: 1,
        label: 'Sandboxes',
        icon: Box,
        chips: [
          { icon: Box, name: 'Sandbox A' },
          { icon: Box, name: 'Sandbox B' },
          { icon: Box, name: 'Sandbox C' },
        ],
      },
    ],
    edges: [
      { from: 'users', to: 'app' },
      { from: 'app', to: 'backend' },
      { from: 'backend', to: 'api' },
      { from: 'api', to: 'agents' },
      { from: 'agents', to: 'sandboxes' },
    ],
  },
  {
    name: 'PR reviewer',
    nodes: [
      {
        id: 'github',
        col: 0,
        row: 0,
        label: 'GitHub',
        icon: GitHubLogo,
        chips: [
          { icon: GitPullRequest, name: 'Pull request opened', short: 'PR opened' },
          { icon: MessageSquareText, name: 'Review posted', short: 'Review' },
        ],
      },
      {
        id: 'webhook',
        col: 1,
        row: 0,
        dy: -30,
        title: 'Webhook',
        calls: ['pull_request.opened'],
      },
      {
        id: 'agent',
        col: 2,
        row: 0,
        chips: [
          { icon: Bot, name: 'Review agent for PR #142' },
          { icon: Bot, name: 'Review agent for PR #143' },
          { icon: Bot, name: 'Review agent for PR #144' },
        ],
      },
      {
        id: 'sandbox',
        col: 2,
        row: 1,
        label: 'Sandbox',
        icon: Box,
        chips: [
          { icon: GitBranch, name: 'Check out the PR' },
          { icon: FlaskConical, name: 'Run the tests' },
        ],
      },
    ],
    edges: [
      { from: 'github', fromChip: 0, to: 'webhook' },
      { from: 'webhook', to: 'agent', toOffset: -6 },
      { from: 'agent', fromOffset: 6, to: 'github', toChip: 1 },
      { from: 'agent', to: 'sandbox' },
    ],
  },
  {
    name: 'Slack agent',
    nodes: [
      {
        id: 'slack',
        col: 0,
        row: 0,
        label: 'Slack',
        icon: SlackLogo,
        chips: [
          { icon: MessageCircleQuestion, name: 'Question in a channel', short: 'Question' },
          { icon: Reply, name: 'Answer in the thread', short: 'Answer' },
        ],
      },
      {
        id: 'agent',
        col: 1,
        row: 0,
        chips: [
          { icon: Bot, name: 'Incident agent for thread 1' },
          { icon: Bot, name: 'Incident agent for thread 2' },
          { icon: Bot, name: 'Incident agent for thread 3' },
        ],
      },
      {
        id: 'tools',
        col: 2,
        row: 0,
        title: 'Tool calls',
        calls: ['search_logs()', 'get_incident()'],
      },
      {
        id: 'mcp',
        col: 2,
        row: 1,
        label: 'Your MCP',
        icon: Plug,
        chips: [
          { icon: Logs, name: 'Internal logs' },
          { icon: Ticket, name: 'Incident tracker' },
        ],
      },
    ],
    edges: [
      { from: 'slack', fromChip: 0, to: 'agent', toOffset: -6 },
      { from: 'agent', fromOffset: 6, to: 'slack', toChip: 1 },
      { from: 'agent', to: 'tools' },
      { from: 'tools', to: 'mcp' },
    ],
  },
]
