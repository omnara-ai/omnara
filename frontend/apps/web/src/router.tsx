import { ApiError, type CurrentUser, type OmnaraClient } from '@omnara/sdk'
import { getCurrentUserOptions } from '@omnara/sdk/tanstack'
import type { QueryClient } from '@tanstack/react-query'
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
} from '@tanstack/react-router'
import { Suspense } from 'react'

import { FullPageSpinner } from '@/components/ui/spinner'
import { RootError } from '@/routes/RootError'

export interface RouterContext {
  queryClient: QueryClient
  omnaraClient: OmnaraClient
}

async function ensureMe(
  queryClient: QueryClient,
  omnaraClient: OmnaraClient,
  returnTo: string,
): Promise<CurrentUser> {
  try {
    return await queryClient.ensureQueryData(getCurrentUserOptions({ client: omnaraClient }))
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
      throw redirect({ href: `/login?return_to=${encodeURIComponent(returnTo)}` })
    }
    throw error
  }
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => (
    <Suspense fallback={<FullPageSpinner />}>
      <Outlet />
    </Suspense>
  ),
  errorComponent: RootError,
})

const authedRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'authed',
  beforeLoad: async ({ context, location }) => {
    const me = await ensureMe(context.queryClient, context.omnaraClient, location.href)
    if (me.orgs.length === 0) {
      // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
      throw redirect({ href: '/onboarding' })
    }
    return { me }
  },
  component: lazyRouteComponent(() => import('@/routes/AuthedLayout'), 'AuthedLayout'),
})

const overviewRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/',
  component: lazyRouteComponent(() => import('@/routes/Overview'), 'Overview'),
})

const membersRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/members',
  component: lazyRouteComponent(() => import('@/routes/Members'), 'Members'),
})

const organizationMachinesRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/machines',
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationMachinesPage'),
    'OrganizationMachinesPage',
  ),
})

const organizationModelsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/models',
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationModelsPage'),
    'OrganizationModelsPage',
  ),
})

const secretsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/secrets',
  component: lazyRouteComponent(() => import('@/routes/SecretsPage'), 'SecretsPage'),
})

const skillsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/skills',
  component: lazyRouteComponent(() => import('@/routes/SkillsPage'), 'SkillsPage'),
})

const apiTokensRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/user/api-tokens',
  component: lazyRouteComponent(() => import('@/routes/ApiTokensPage'), 'ApiTokensPage'),
})

const projectRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId',
  beforeLoad: ({ params }) => {
    // The project root has no page of its own; land on the agents list.
    // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
    throw redirect({ to: '/projects/$projectId/agents', params })
  },
})

const projectAgentsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/agents',
  component: lazyRouteComponent(() => import('@/routes/ProjectAgentsPage'), 'ProjectAgentsPage'),
})

const projectGrantsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/grants',
  component: lazyRouteComponent(() => import('@/routes/ProjectGrantsPage'), 'ProjectGrantsPage'),
})

const projectSecretsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/secrets',
  component: lazyRouteComponent(() => import('@/routes/ProjectSecretsPage'), 'ProjectSecretsPage'),
})

const projectSkillsRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/skills',
  component: lazyRouteComponent(() => import('@/routes/ProjectSkillsPage'), 'ProjectSkillsPage'),
})

const agentProfileBuilderRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/agent-profiles/new',
  component: lazyRouteComponent(
    () => import('@/routes/AgentProfileBuilder'),
    'AgentProfileBuilder',
  ),
})

const createAgentRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/agents/new',
  component: lazyRouteComponent(() => import('@/routes/CreateAgentPage'), 'CreateAgentPage'),
})

const agentRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/projects/$projectId/agents/$agentId',
  component: lazyRouteComponent(() => import('@/routes/AgentView'), 'AgentView'),
})

const deviceAuthRoute = createRoute({
  getParentRoute: () => authedRoute,
  path: '/device',
  component: lazyRouteComponent(() => import('@/routes/DeviceAuth'), 'DeviceAuth'),
})

const onboardingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/onboarding',
  beforeLoad: async ({ context, location }) => {
    const me = await ensureMe(context.queryClient, context.omnaraClient, location.href)
    if (me.orgs.length > 0) {
      // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
      throw redirect({ href: '/' })
    }
  },
  component: lazyRouteComponent(() => import('@/routes/Onboarding'), 'Onboarding'),
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: lazyRouteComponent(() => import('@/routes/Login'), 'Login'),
})

const signupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/signup',
  component: lazyRouteComponent(() => import('@/routes/SignUp'), 'SignUp'),
})

const verifyEmailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/verify-email',
  component: lazyRouteComponent(() => import('@/routes/VerifyEmail'), 'VerifyEmail'),
})

const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/reset-password',
  component: lazyRouteComponent(() => import('@/routes/ResetPassword'), 'ResetPassword'),
})

const forgotPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forgot-password',
  component: lazyRouteComponent(() => import('@/routes/ForgotPassword'), 'ForgotPassword'),
})

const routeTree = rootRoute.addChildren([
  loginRoute,
  signupRoute,
  verifyEmailRoute,
  resetPasswordRoute,
  forgotPasswordRoute,
  onboardingRoute,
  authedRoute.addChildren([
    overviewRoute,
    membersRoute,
    organizationMachinesRoute,
    organizationModelsRoute,
    secretsRoute,
    skillsRoute,
    apiTokensRoute,
    projectRoute,
    projectAgentsRoute,
    projectGrantsRoute,
    projectSecretsRoute,
    projectSkillsRoute,
    agentProfileBuilderRoute,
    createAgentRoute,
    agentRoute,
    deviceAuthRoute,
  ]),
])

export const router = createRouter({
  routeTree,
  context: {
    queryClient: undefined as unknown as QueryClient,
    omnaraClient: undefined as unknown as OmnaraClient,
  },
  defaultPreload: 'intent',
  defaultPendingComponent: FullPageSpinner,
  defaultErrorComponent: RootError,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
