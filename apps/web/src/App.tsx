
import { Toaster } from "@/components/ui/toaster";
import { Toaster as Sonner } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Routes, Route } from "react-router-dom";
import { AuthProvider } from "./lib/auth/AuthContext";
import { ProtectedRoute } from "./lib/auth/ProtectedRoute";
import { ThemeProvider } from "./lib/theme/ThemeProvider";
import Index from "./pages/Index";
import NotFound from "./pages/NotFound";
import Privacy from "./pages/Privacy";
import Terms from "./pages/Terms";
import { SidebarDashboardLayout } from "./components/dashboard";
import { 
  LazyRoute,
  CommandCenter,
  AllInstances,
  UserAgents,
  Pricing,
  Billing,
  Settings,
  InstanceDetail,
  InstanceList,
  APIKeyManagement
} from "./components/LazyLoader";
import CLIAuth from "./pages/CLIAuth";

const queryClient = new QueryClient();

const App = () => (
  <QueryClientProvider client={queryClient}>
    <ThemeProvider defaultTheme="dark">
      <AuthProvider>
        <TooltipProvider>
        <Toaster />
        <Sonner />
        <BrowserRouter>
        <Routes>
          <Route path="/" element={<Index />} />
          <Route path="/pricing" element={<LazyRoute component={Pricing} />} />
          <Route path="/privacy" element={<Privacy />} />
          <Route path="/terms" element={<Terms />} />
          <Route path="/cli-auth" element={<CLIAuth />} />
          
          {/* Protected dashboard routes */}
          <Route
            path="/dashboard"
            element={
              <ProtectedRoute>
                <SidebarDashboardLayout />
              </ProtectedRoute>
            }
          >
            {/* New Command Center as default dashboard */}
            <Route index element={<LazyRoute component={CommandCenter} />} />
            <Route path="instances" element={<LazyRoute component={AllInstances} />} />
            <Route path="instances/:instanceId" element={<LazyRoute component={InstanceDetail} />} />
            {/* <Route path="analytics" element={<Analytics />} /> */}
            <Route path="api-keys" element={<LazyRoute component={APIKeyManagement} />} />
            {/* Redirect user-agents to dashboard since management is now integrated */}
            <Route path="user-agents" element={<LazyRoute component={CommandCenter} />} />
            <Route path="user-agents/:agentId/instances" element={<LazyRoute component={InstanceList} />} />
            <Route path="billing" element={<LazyRoute component={Billing} />} />
            <Route path="settings" element={<LazyRoute component={Settings} />} />
          </Route>
          
          {/* ADD ALL CUSTOM ROUTES ABOVE THE CATCH-ALL "*" ROUTE */}
          <Route path="*" element={<NotFound />} />
        </Routes>
        </BrowserRouter>
        </TooltipProvider>
      </AuthProvider>
    </ThemeProvider>
  </QueryClientProvider>
);

export default App;
