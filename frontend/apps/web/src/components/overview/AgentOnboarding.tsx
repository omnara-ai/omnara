import { useCreateAgentConfig, useCreateAgentProfile } from '@omnara/react'
import type { AgentProfile, VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { Monitor, Terminal } from '@/components/icons'
import { CodeBlock, CodeTabsBlock } from '@/components/overview/CodeBlock'
import { ChatGuide, ChatOptions, CreateChat, RunFooter } from '@/components/overview/OnboardingChat'
import {
  chatMessage,
  cliLoginCommand,
  cliSetupPrompt,
  cliTokenHost,
  profileCreateSpec,
} from '@/components/overview/onboardingCli'
import { ProfileDraftStep } from '@/components/overview/OnboardingProfileDraft'
import { OnboardingStep, OnboardingSteps } from '@/components/overview/OnboardingSteps'
import { useCreateChat } from '@/components/overview/useCreateChat'
import { useOnboarding } from '@/components/overview/useOnboarding'
import { useTemplateDefaults } from '@/components/overview/useTemplateDefaults'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { errorMessage } from '@/lib/submit-status'

type OnboardingTab = 'cli' | 'browser'

export function AgentOnboarding({ orgId, project }: { orgId: string; project: VisibleProject }) {
  const [tab, setTab] = useState<OnboardingTab>('cli')

  return (
    <div className="flex w-full max-w-4xl flex-col gap-10 py-4">
      <Tabs
        value={tab}
        onValueChange={(value) => {
          setTab(value === 'browser' ? 'browser' : 'cli')
        }}
      >
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-2">
            <h2 className="text-2xl font-semibold tracking-tight">Launch your first agent</h2>
            <p className="text-muted-foreground text-sm">
              Follow the steps to create an agent profile and start a chat.
            </p>
          </div>
          <TabsList aria-label="Setup method" className="h-10 self-start">
            <TabsTrigger value="cli" className="px-4">
              <Terminal />
              CLI
            </TabsTrigger>
            <TabsTrigger value="browser" className="px-4">
              <Monitor />
              Browser
            </TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="cli" className="pt-14">
          <CliSteps orgId={orgId} project={project} />
        </TabsContent>
        <TabsContent value="browser" className="pt-14">
          <BrowserSteps orgId={orgId} project={project} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function CliSteps({ orgId, project }: { orgId: string; project: VisibleProject }) {
  const {
    ready,
    cliToken,
    agentProfile: profile,
    agent,
    live,
    steps,
  } = useOnboarding({
    orgId,
    projectId: project.id,
  })
  const [profilePending, setProfilePending] = useState(false)
  const chatRun = useCreateChat(orgId, project, profile)
  const { login, createProfile, chat } = steps.cli
  if (!ready) return null
  return (
    <OnboardingSteps>
      <OnboardingStep
        index={1}
        title="Log in with the CLI"
        doneTitle="Logged in with the CLI"
        description="Run the command below to sign in and save an API token"
        status={login}
        nextStatus={createProfile}
        completion={cliToken ? cliTokenHost(cliToken) : 'CLI access is set up'}
      >
        {login !== 'done' && <CodeBlock content={cliLoginCommand} label="command" />}
      </OnboardingStep>
      <OnboardingStep
        index={2}
        title="Create an agent profile"
        doneTitle="Created an agent profile"
        description="Run the command or hand the prompt to your coding agent"
        status={createProfile}
        nextStatus={chat}
        pending={profilePending}
        completion={profile && <ProfileCreated project={project} profile={profile} />}
      >
        {!profile && (
          <CliProfileOptions
            orgId={orgId}
            projectId={project.id}
            disabled={live.agentProfile != null}
            onPendingChange={setProfilePending}
          />
        )}
      </OnboardingStep>
      <OnboardingStep
        index={3}
        title="Chat with your agent"
        doneTitle="Started a chat"
        description="Launch an agent from the profile, then talk to it from the browser or your terminal"
        status={chat}
        pending={chatRun.pending}
        completion={agent && <ChatGuide project={project} agent={agent} />}
      >
        {profile ? (
          <ChatOptions
            orgId={orgId}
            project={project}
            profile={profile}
            run={
              agent
                ? undefined
                : {
                    pending: chatRun.pending || live.agent != null,
                    error: chatRun.error,
                    onRun: () => {
                      void chatRun.launch(chatMessage)
                    },
                  }
            }
          />
        ) : null}
      </OnboardingStep>
    </OnboardingSteps>
  )
}

function CliProfileOptions({
  orgId,
  projectId,
  disabled,
  onPendingChange,
}: {
  orgId: string
  projectId: string
  disabled: boolean
  onPendingChange: (pending: boolean) => void
}) {
  const defaults = useTemplateDefaults(orgId, projectId)
  const createAgentConfig = useCreateAgentConfig(orgId, projectId)
  const createAgentProfile = useCreateAgentProfile(orgId, projectId)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<ReactNode>(null)
  if (!defaults.ready) return null
  const template = agentTemplates[0]
  if (!template) return null
  const spec = profileCreateSpec({
    orgId,
    projectId,
    name: template.name,
    config: defaults.build(template),
  })

  async function run() {
    setPending(true)
    onPendingChange(true)
    setError(null)
    try {
      const config = await createAgentConfig.mutateAsync({
        source: spec.json,
        source_format: 'json',
      })
      await createAgentProfile.mutateAsync({ name: spec.name, config: config.id })
    } catch (err) {
      setError(errorMessage(err, 'Could not create profile'))
    }
    setPending(false)
    onPendingChange(false)
  }

  return (
    <CodeTabsBlock
      label="How to create the profile"
      tabs={[
        { value: 'command', label: 'CLI', content: spec.command },
        {
          value: 'prompt',
          label: 'Prompt',
          content: { copy: cliSetupPrompt, segments: [cliSetupPrompt], language: 'text' },
          emphasis: true,
          footer: false,
        },
      ]}
      footer={
        <RunFooter
          pending={pending || disabled}
          error={error}
          onRun={() => {
            void run()
          }}
        />
      }
    />
  )
}

function BrowserSteps({ orgId, project }: { orgId: string; project: VisibleProject }) {
  const {
    agentProfile: profile,
    agent,
    live,
    steps,
  } = useOnboarding({ orgId, projectId: project.id })
  const { createProfile, chat } = steps.browser
  return (
    <OnboardingSteps>
      <OnboardingStep
        index={1}
        title="Create an agent profile"
        doneTitle="Created an agent profile"
        description="Pick a template, optionally customize it, then create the profile"
        status={createProfile}
        nextStatus={chat}
        completion={profile && <ProfileCreated project={project} profile={profile} />}
      >
        {!profile && (
          <ProfileDraftStep orgId={orgId} project={project} disabled={live.agentProfile != null} />
        )}
      </OnboardingStep>
      <OnboardingStep
        index={2}
        title="Create a chat"
        doneTitle="Created a chat"
        description="Start a chat with an agent running your profile"
        status={chat}
        completion={agent && <ChatGuide project={project} agent={agent} />}
      >
        {agent ? null : profile ? (
          <CreateChat
            orgId={orgId}
            project={project}
            profile={profile}
            disabled={live.agent != null}
          />
        ) : null}
        {agent && profile && <ChatOptions orgId={orgId} project={project} profile={profile} />}
      </OnboardingStep>
    </OnboardingSteps>
  )
}

function ProfileCreated({ project, profile }: { project: VisibleProject; profile: AgentProfile }) {
  return (
    <>
      <Link
        to="/projects/$projectId/agent-profiles/$profileId"
        params={{ projectId: project.id, profileId: profile.id }}
        className="underline-offset-2 hover:underline"
      >
        {profile.name}
      </Link>
    </>
  )
}
