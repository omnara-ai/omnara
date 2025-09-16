import { lazy, Suspense } from 'react';
import { Loader2 } from 'lucide-react';

// Loading component
export function PageLoader() {
  return (
    <div className="flex items-center justify-center min-h-[400px]">
      <Loader2 className="h-8 w-8 animate-spin text-primary" />
    </div>
  );
}

// Lazy load heavy components
export const CommandCenter = lazy(() => import('@/pages/CommandCenter'));
export const AllInstances = lazy(() => import('@/pages/AllInstances'));
export const UserAgents = lazy(() => import('@/pages/UserAgents'));
export const Pricing = lazy(() => import('@/pages/Pricing'));
export const Billing = lazy(() => import('@/pages/dashboard/Billing'));
export const Settings = lazy(() => import('@/pages/dashboard/Settings'));
export const InstanceDetail = lazy(() => 
  import('@/components/dashboard').then(module => ({ 
    default: module.InstanceDetail 
  }))
);
export const InstanceList = lazy(() => 
  import('@/components/dashboard').then(module => ({ 
    default: module.InstanceList 
  }))
);
export const APIKeyManagement = lazy(() => 
  import('@/components/dashboard').then(module => ({ 
    default: module.APIKeyManagement 
  }))
);

// Wrapper component for lazy loaded routes
export function LazyRoute({ component: Component }: { component: React.ComponentType }) {
  return (
    <Suspense fallback={<PageLoader />}>
      <Component />
    </Suspense>
  );
}