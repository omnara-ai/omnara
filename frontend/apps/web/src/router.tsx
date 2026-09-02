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
import { safeReturnTo } from '@/lib/auth-return-to'
import { requireOrganization } from '@/lib/require-organization'
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

const authenticatedRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'authenticated',
  beforeLoad: async ({ context, location }) => {
    const me = await ensureMe(context.queryClient, context.omnaraClient, location.href)
    return { me }
  },
  component: Outlet,
})

const onboardedRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  id: 'onboarded',
  beforeLoad: ({ context, location }) => {
    requireOrganization(context.me, location.href)
  },
  component: lazyRouteComponent(() => import('@/routes/AuthedLayout'), 'AuthedLayout'),
})

const overviewRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/',
  component: lazyRouteComponent(() => import('@/routes/Overview'), 'Overview'),
})

const membersRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/members',
  component: lazyRouteComponent(() => import('@/routes/Members'), 'Members'),
})

const organizationMachinesRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/machines',
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationMachinesPage'),
    'OrganizationMachinesPage',
  ),
})

const organizationModelsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/models',
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationModelsPage'),
    'OrganizationModelsPage',
  ),
})

const secretsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/secrets',
  component: lazyRouteComponent(() => import('@/routes/SecretsPage'), 'SecretsPage'),
})

const skillsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/skills',
  component: lazyRouteComponent(() => import('@/routes/SkillsPage'), 'SkillsPage'),
})

const apiTokensRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/user/api-tokens',
  component: lazyRouteComponent(() => import('@/routes/ApiTokensPage'), 'ApiTokensPage'),
})

const projectRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId',
  beforeLoad: ({ params }) => {
    // The project root has no page of its own; land on the agents list.
    // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
    throw redirect({ to: '/projects/$projectId/agents', params })
  },
})

const projectAgentsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agents',
  component: lazyRouteComponent(() => import('@/routes/ProjectAgentsPage'), 'ProjectAgentsPage'),
})

const projectGrantsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/grants',
  component: lazyRouteComponent(() => import('@/routes/ProjectGrantsPage'), 'ProjectGrantsPage'),
})

const projectSecretsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/secrets',
  component: lazyRouteComponent(() => import('@/routes/ProjectSecretsPage'), 'ProjectSecretsPage'),
})

const projectSkillsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/skills',
  component: lazyRouteComponent(() => import('@/routes/ProjectSkillsPage'), 'ProjectSkillsPage'),
})

const agentProfileRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agent-profiles/$profileId',
  component: lazyRouteComponent(() => import('@/routes/AgentProfileView'), 'AgentProfileView'),
})

const createAgentRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agents/new',
  validateSearch: (search: Record<string, unknown>): { template?: string } =>
    typeof search.template === 'string' ? { template: search.template } : {},
  component: lazyRouteComponent(() => import('@/routes/CreateAgentPage'), 'CreateAgentPage'),
})

const agentRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agents/$agentId',
  component: lazyRouteComponent(() => import('@/routes/AgentView'), 'AgentView'),
})

const deviceAuthRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: '/device',
  beforeLoad: ({ context, location }) => {
    requireOrganization(context.me, location.href)
  },
  component: lazyRouteComponent(() => import('@/routes/DeviceAuth'), 'DeviceAuth'),
})

const onboardingRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: '/onboarding',
  beforeLoad: ({ context, location }) => {
    if (context.me.orgs.length > 0) {
      const returnTo = new URL(location.href, window.location.origin).searchParams.get('return_to')
      // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
      throw redirect({ href: safeReturnTo(returnTo) })
    }
  },
  component: lazyRouteComponent(() => import('@/routes/Onboarding'), 'Onboarding'),
})

const invitationsRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: '/invitations',
  component: lazyRouteComponent(() => import('@/routes/Invitations'), 'Invitations'),
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
  authenticatedRoute.addChildren([
    deviceAuthRoute,
    onboardingRoute,
    invitationsRoute,
    onboardedRoute.addChildren([
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
      agentProfileRoute,
      createAgentRoute,
      agentRoute,
    ]),
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
