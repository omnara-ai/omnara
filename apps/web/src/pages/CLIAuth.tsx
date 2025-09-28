import { useEffect, useState, useCallback } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useAuth } from "@/lib/auth/AuthContext";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Loader2, Terminal, CheckCircle2, XCircle, Copy, Monitor, Globe, Eye, EyeOff } from "lucide-react";
import { useToast } from "@/hooks/use-toast";
import { GhibliBackground } from "@/components/dashboard";
import { RetroCard, RetroCardContent, RetroCardHeader, RetroCardTitle } from "@/components/ui/RetroCard";
import { AuthModal } from "@/components/AuthModal";
import { apiClient } from "@/lib/dashboardApi";

export default function CLIAuth() {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const { user } = useAuth();
  const { toast } = useToast();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);
  const [apiKey, setApiKey] = useState<string | null>(null);
  const [localAuthAttempted, setLocalAuthAttempted] = useState(false);
  const [copied, setCopied] = useState(false);
  const [showApiKey, setShowApiKey] = useState(false);
  const [isGenerating, setIsGenerating] = useState(false);

  const port = searchParams.get("port");
  const state = searchParams.get("state");

  useEffect(() => {
    // Show modal only if we have params and no user
    if (port && state && user === null) {
      setShowAuthModal(true);
    } else if (user) {
      // Hide modal when user is authenticated
      setShowAuthModal(false);
    }
  }, [port, state, user]);

  const handleAuthSuccess = () => {
    setShowAuthModal(false);
    // The page will re-render and the user effect will trigger key generation
  };

  const generateCLIKey = useCallback(async () => {
    if (!state) {
      setError("Missing state parameter");
      return;
    }

    setLoading(true);
    setError(null);

    try {
      // Call the backend to create a CLI key using the API client
      const data = await apiClient.createCLIKey();
      const key = data.api_key;
      setApiKey(key);
      
      toast({
        title: "Success!",
        description: "API key generated successfully",
      });

      return key;
    } catch (err) {
      console.error("Error generating CLI key:", err);
      setError(err instanceof Error ? err.message : "Failed to generate CLI key");
      toast({
        title: "Error",
        description: err instanceof Error ? err.message : "Failed to generate CLI key",
        variant: "destructive",
      });
      return null;
    } finally {
      setLoading(false);
    }
  }, [state]); // eslint-disable-line react-hooks/exhaustive-deps

  const authenticateLocalCLI = async () => {
    if (!port || !state || !apiKey) return;
    
    setLocalAuthAttempted(true);
    
    try {
      // Try to send the API key to the local CLI server using window.location
      const callbackUrl = `http://127.0.0.1:${port}/?api_key=${encodeURIComponent(apiKey)}&state=${encodeURIComponent(state)}`;
      
      // Open in a hidden iframe or new window to bypass CSP
      const iframe = document.createElement('iframe');
      iframe.style.display = 'none';
      iframe.src = callbackUrl;
      document.body.appendChild(iframe);
      
      // Remove iframe after a short delay
      setTimeout(() => {
        document.body.removeChild(iframe);
      }, 1000);
      
      setSuccess(true);
      toast({
        title: "Success!",
        description: "Local CLI authenticated successfully",
      });
      
      // Wait 2 seconds then redirect to dashboard
      setTimeout(() => {
        navigate("/dashboard");
      }, 2000);
    } catch (err) {
      // This might fail for remote users, which is expected
      console.log("Local authentication failed (expected for remote users):", err);
    }
  };

  const copyApiKey = () => {
    if (!apiKey) return;
    
    navigator.clipboard.writeText(apiKey);
    setCopied(true);
    toast({
      title: "Copied!",
      description: "API key copied to clipboard",
    });
    
    setTimeout(() => setCopied(false), 2000);
  };

  // Auto-generate key on mount if parameters are valid and user is authenticated
  useEffect(() => {
    if (state && user && !apiKey && !isGenerating) {
      setIsGenerating(true);
      generateCLIKey();
    }
  }, [state, user, apiKey, isGenerating, generateCLIKey]);

  // Auto-authenticate local CLI once we have a key (best effort)
  useEffect(() => {
    // If running locally and we have everything we need, attempt local auth automatically
    if (apiKey && port && state && !localAuthAttempted && !success) {
      authenticateLocalCLI();
    }
  }, [apiKey, port, state, localAuthAttempted, success]);

  if (!state) {
    return (
      <div className="min-h-screen relative">
        <GhibliBackground />
        <div className="relative z-10 flex items-center justify-center min-h-screen p-4">
          <RetroCard className="max-w-md w-full" variant="terminal" glow>
            <RetroCardHeader>
              <RetroCardTitle className="flex items-center gap-2">
                <XCircle className="h-5 w-5 text-terracotta" />
                Invalid Request
              </RetroCardTitle>
            </RetroCardHeader>
            <RetroCardContent>
              <p className="text-cream/80 mb-4">
                Missing required parameters for CLI authentication.
              </p>
              <Alert className="bg-terracotta/20 border-terracotta/40 text-cream">
                <AlertDescription>
                  This page should be accessed from the Omnara CLI. Please run the CLI command again.
                </AlertDescription>
              </Alert>
              <Button 
                className="w-full mt-4 bg-cozy-amber hover:bg-soft-gold text-warm-charcoal font-semibold transition-all duration-300 hover-lift" 
                onClick={() => navigate("/dashboard")}
              >
                Go to App
              </Button>
            </RetroCardContent>
          </RetroCard>
        </div>
      </div>
    );
  }

  return (
    <>
      <div className="min-h-screen relative">
        <GhibliBackground />
        <div className="relative z-10 flex items-center justify-center min-h-screen p-4">
          <RetroCard className="max-w-md w-full" variant="terminal" glow>
            <RetroCardHeader>
              <RetroCardTitle className="flex items-center gap-2">
                <Terminal className="h-5 w-5 text-retro-terminal" />
                Omnara CLI Authentication
              </RetroCardTitle>
              <p className="text-cream/60 text-sm mt-2">
                Authorizing your CLI to access Omnara
              </p>
            </RetroCardHeader>
            <RetroCardContent>
              {loading && (
                <div className="flex flex-col items-center gap-4 py-8">
                  <div className="relative">
                    <Loader2 className="h-8 w-8 animate-spin text-cozy-amber" />
                    <div className="absolute inset-0 h-8 w-8 animate-spin text-cozy-amber/30 blur-lg" />
                  </div>
                  <p className="text-sm text-cream/60">Generating your API key...</p>
                </div>
              )}

              {success && (
                <div className="flex flex-col items-center gap-4 py-8 animate-fade-in">
                  <CheckCircle2 className="h-8 w-8 text-sage-green animate-scale-in" />
                  <p className="text-sm text-cream/80">Local CLI authenticated successfully!</p>
                  <p className="text-xs text-cream/60 mt-2">You can close this window and return to your terminal.</p>
                </div>
              )}

              {error && (
                <>
                  <Alert className="bg-terracotta/20 border-terracotta/40 text-cream mb-4">
                    <AlertDescription>{error}</AlertDescription>
                  </Alert>
                  <div className="flex gap-2">
                    <Button 
                      onClick={() => generateCLIKey()} 
                      disabled={loading}
                      className="flex-1 bg-cozy-amber hover:bg-soft-gold text-warm-charcoal font-semibold transition-all duration-300 hover-lift disabled:opacity-50"
                    >
                      {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                      Try Again
                    </Button>
                    <Button 
                      variant="outline" 
                      onClick={() => navigate("/dashboard")}
                      className="flex-1 border-cozy-amber/30 text-cream hover:bg-cozy-amber/10 hover:border-cozy-amber/50 transition-all duration-300"
                    >
                      Go to Dashboard
                    </Button>
                  </div>
                </>
              )}

              {!loading && !error && !success && apiKey && (
                <div className="space-y-6">
                  <div className="text-center">
                    <p className="text-sm text-cream/80 mb-4">
                      Choose how to authenticate your CLI:
                    </p>
                  </div>

                  {/* Local CLI Authentication */}
                  <div className="space-y-3">
                    <div className="flex items-center gap-2 text-cream/60 text-sm">
                      <Monitor className="h-4 w-4" />
                      <span>Running CLI locally on this machine?</span>
                    </div>
                    <Button
                      onClick={authenticateLocalCLI}
                      disabled={localAuthAttempted}
                      className="w-full bg-cozy-amber hover:bg-soft-gold text-warm-charcoal font-semibold transition-all duration-300 hover-lift disabled:opacity-50"
                    >
                      {localAuthAttempted ? (
                        <>
                          <CheckCircle2 className="mr-2 h-4 w-4" />
                          Authentication Attempted
                        </>
                      ) : (
                        <>
                          <Monitor className="mr-2 h-4 w-4" />
                          Authenticate Local CLI
                        </>
                      )}
                    </Button>
                    {localAuthAttempted && !success && (
                      <p className="text-xs text-cream/50 text-center">
                        If nothing happened, use the remote option below
                      </p>
                    )}
                  </div>

                  <div className="relative">
                    <div className="absolute inset-0 flex items-center">
                      <span className="w-full border-t border-cream/20" />
                    </div>
                    <div className="relative flex justify-center text-xs uppercase">
                      <span className="bg-warm-charcoal px-2 text-cream/50">or</span>
                    </div>
                  </div>

                  {/* Remote CLI Authentication */}
                  <div className="space-y-3">
                    <div className="flex items-center gap-2 text-cream/60 text-sm">
                      <Globe className="h-4 w-4" />
                      <span>Running CLI on a remote server (SSH)?</span>
                    </div>
                    <div className="bg-night-sky/50 border border-cozy-amber/20 rounded-lg p-4">
                      <p className="text-xs text-cream/60 mb-3">Copy this API key and paste it in your terminal:</p>
                      <div className="flex gap-2">
                        <Input
                          type={showApiKey ? "text" : "password"}
                          value={apiKey}
                          readOnly
                          className="flex-1 bg-warm-charcoal/50 border border-cozy-amber/30 text-retro-terminal font-mono text-xs"
                        />
                        <Button
                          onClick={() => setShowApiKey(!showApiKey)}
                          variant="outline"
                          size="sm"
                          className="bg-warm-charcoal/50 border-cozy-amber/50 text-cozy-amber/70 hover:bg-cozy-amber/20 hover:text-cozy-amber hover:border-cozy-amber/70 transition-all"
                        >
                          {showApiKey ? (
                            <EyeOff className="h-4 w-4" />
                          ) : (
                            <Eye className="h-4 w-4" />
                          )}
                        </Button>
                        <Button
                          onClick={copyApiKey}
                          variant="outline"
                          size="sm"
                          className="bg-warm-charcoal/50 border-cozy-amber/50 text-cozy-amber/70 hover:bg-cozy-amber/20 hover:text-cozy-amber hover:border-cozy-amber/70 transition-all"
                        >
                          {copied ? (
                            <CheckCircle2 className="h-4 w-4 text-sage-green/80" />
                          ) : (
                            <Copy className="h-4 w-4" />
                          )}
                        </Button>
                      </div>
                    </div>
                  </div>
                </div>
              )}

              {!loading && !error && !success && !apiKey && (
                <div className="text-center py-8">
                  <div className="inline-block">
                    <Terminal className="h-12 w-12 text-retro-terminal/40 mx-auto mb-4" />
                  </div>
                  <p className="text-sm text-cream/60">
                    Preparing authentication options...
                  </p>
                </div>
              )}
            </RetroCardContent>
          </RetroCard>
        </div>
      </div>

      <AuthModal
        isOpen={showAuthModal}
        onClose={() => setShowAuthModal(false)}
        onSuccess={handleAuthSuccess}
        redirectTo={window.location.href}
      />
    </>
  );
}
