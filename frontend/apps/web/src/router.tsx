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
import { z } from 'zod'

import { FullPageSpinner } from '@/components/ui/spinner'
import { safeReturnTo } from '@/lib/auth-return-to'
import { queryClient } from '@/lib/query'
import { requireOrganization } from '@/lib/require-organization'
import { RootError } from '@/routes/RootError'
import { omnaraClient } from '@/transport'

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

const organizationModelsSearch = z.object({ provider: z.string().optional().catch(undefined) })

const organizationModelsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/models',
  validateSearch: organizationModelsSearch,
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationModelsPage'),
    'OrganizationModelsPage',
  ),
})

const organizationUsageRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/usage',
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationUsagePage'),
    'OrganizationUsagePage',
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

// Not under onboardedRoute: users with no organization must still be able to
// reach their account, e.g. to delete it.
const accountSettingsRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: '/user/account',
  component: lazyRouteComponent(
    () => import('@/routes/AccountSettingsPage'),
    'AccountSettingsPage',
  ),
})

const organizationSettingsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/settings',
  component: lazyRouteComponent(
    () => import('@/routes/OrganizationSettingsPage'),
    'OrganizationSettingsPage',
  ),
})

const projectRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId',
  component: lazyRouteComponent(
    () => import('@/routes/ProjectOverviewPage'),
    'ProjectOverviewPage',
  ),
})

const agentsTabSearch = z.object({
  tab: z.enum(['profiles', 'instances']).optional().catch(undefined),
})

const organizationAgentsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/agents',
  validateSearch: agentsTabSearch,
  component: lazyRouteComponent(() => import('@/routes/OrgAgentsPage'), 'OrgAgentsPage'),
})

const projectAgentsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agents',
  validateSearch: agentsTabSearch,
  component: lazyRouteComponent(() => import('@/routes/ProjectAgentsPage'), 'ProjectAgentsPage'),
})

const integrationsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/integrations',
  component: lazyRouteComponent(() => import('@/routes/IntegrationsPage'), 'IntegrationsPage'),
})

const integrationCatalogRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/integrations/new',
  component: lazyRouteComponent(
    () => import('@/routes/CreateIntegrationPage'),
    'CreateIntegrationPage',
  ),
})

const createIntegrationRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/integrations/new/$integrationKind',
  component: lazyRouteComponent(
    () => import('@/routes/CreateIntegrationPage'),
    'CreateIntegrationPage',
  ),
})

const integrationDetailRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/integrations/$integrationId',
  component: lazyRouteComponent(() => import('@/routes/IntegrationPage'), 'IntegrationPage'),
})

const projectModelsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/models',
  component: lazyRouteComponent(() => import('@/routes/ProjectModelsPage'), 'ProjectModelsPage'),
})

const projectMachinesRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/machines',
  component: lazyRouteComponent(
    () => import('@/routes/ProjectMachinesPage'),
    'ProjectMachinesPage',
  ),
})

const projectSharingSearch = z.object({
  tab: z.enum(['project', 'shared']).optional().catch(undefined),
})

const projectSecretsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/secrets',
  validateSearch: projectSharingSearch,
  component: lazyRouteComponent(() => import('@/routes/ProjectSecretsPage'), 'ProjectSecretsPage'),
})

const projectSkillsRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/skills',
  validateSearch: projectSharingSearch,
  component: lazyRouteComponent(() => import('@/routes/ProjectSkillsPage'), 'ProjectSkillsPage'),
})

const projectMemoryRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/memory',
  component: lazyRouteComponent(() => import('@/routes/ProjectMemoryPage'), 'ProjectMemoryPage'),
})

const memoryStoreRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/memory/$storeId',
  validateSearch: z.object({ path: z.string().optional(), folder: z.string().optional() }),
  component: lazyRouteComponent(() => import('@/routes/MemoryStorePage'), 'MemoryStorePage'),
})

const projectUsageRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/usage',
  component: lazyRouteComponent(() => import('@/routes/ProjectUsagePage'), 'ProjectUsagePage'),
})

const agentProfileRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agent-profiles/$profileId',
  component: lazyRouteComponent(() => import('@/routes/AgentProfileView'), 'AgentProfileView'),
})

const agentProfileIndexRoute = createRoute({
  getParentRoute: () => agentProfileRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
    throw redirect({ to: '/projects/$projectId/agent-profiles/$profileId/configuration', params })
  },
})

const agentProfileConfigurationRoute = createRoute({
  getParentRoute: () => agentProfileRoute,
  path: '/configuration',
})

const agentProfileSchedulesRoute = createRoute({
  getParentRoute: () => agentProfileRoute,
  path: '/schedules',
})

const agentProfileAgentsRoute = createRoute({
  getParentRoute: () => agentProfileRoute,
  path: '/agents',
})

const agentProfileUsageRoute = createRoute({
  getParentRoute: () => agentProfileRoute,
  path: '/usage',
})

const createAgentSearch = z.object({ template: z.string().optional().catch(undefined) })

const createAgentRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agents/new',
  validateSearch: createAgentSearch,
  component: lazyRouteComponent(() => import('@/routes/CreateAgentPage'), 'CreateAgentPage'),
})

const agentSearch = z.object({ config: z.boolean().optional().catch(undefined) })

const agentRoute = createRoute({
  getParentRoute: () => onboardedRoute,
  path: '/projects/$projectId/agents/$agentId',
  validateSearch: agentSearch,
  component: lazyRouteComponent(() => import('@/routes/AgentView'), 'AgentView'),
})

const agentIndexRoute = createRoute({
  getParentRoute: () => agentRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    // eslint-disable-next-line @typescript-eslint/only-throw-error -- TanStack Router throws redirects.
    throw redirect({ to: '/projects/$projectId/agents/$agentId/events', params })
  },
})

const agentEventsRoute = createRoute({
  getParentRoute: () => agentRoute,
  path: '/events',
})

const agentChatRoute = createRoute({
  getParentRoute: () => agentRoute,
  path: '/chat',
})

const deviceAuthRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: '/device',
  beforeLoad: ({ context, location }) => {
    requireOrganization(context.me, location.href)
  },
  component: lazyRouteComponent(() => import('@/routes/DeviceAuth'), 'DeviceAuth'),
})

const oauthAuthorizeRoute = createRoute({
  getParentRoute: () => authenticatedRoute,
  path: '/oauth/authorize',
  beforeLoad: ({ context, location }) => {
    requireOrganization(context.me, location.href)
  },
  component: lazyRouteComponent(() => import('@/routes/OAuthAuthorize'), 'OAuthAuthorize'),
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
    oauthAuthorizeRoute,
    onboardingRoute,
    invitationsRoute,
    accountSettingsRoute,
    onboardedRoute.addChildren([
      overviewRoute,
      membersRoute,
      organizationMachinesRoute,
      organizationModelsRoute,
      organizationAgentsRoute,
      organizationUsageRoute,
      secretsRoute,
      skillsRoute,
      apiTokensRoute,
      organizationSettingsRoute,
      projectRoute,
      projectAgentsRoute,
      integrationsRoute,
      integrationCatalogRoute,
      createIntegrationRoute,
      integrationDetailRoute,
      projectModelsRoute,
      projectMachinesRoute,
      projectSecretsRoute,
      projectSkillsRoute,
      projectMemoryRoute,
      memoryStoreRoute,
      projectUsageRoute,
      agentProfileRoute.addChildren([
        agentProfileIndexRoute,
        agentProfileConfigurationRoute,
        agentProfileSchedulesRoute,
        agentProfileAgentsRoute,
        agentProfileUsageRoute,
      ]),
      createAgentRoute,
      agentRoute.addChildren([agentIndexRoute, agentEventsRoute, agentChatRoute]),
    ]),
  ]),
])

export const router = createRouter({
  routeTree,
  context: { queryClient, omnaraClient },
  defaultPreload: 'intent',
  defaultPendingComponent: FullPageSpinner,
  defaultErrorComponent: RootError,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
