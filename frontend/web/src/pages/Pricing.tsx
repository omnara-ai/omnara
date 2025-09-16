import { Check } from 'lucide-react';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card';
import { useAuth } from '@/lib/auth/AuthContext';
import { useSubscription } from '@/hooks/useSubscription';
import { PLANS } from '@/lib/stripe';
import { billingApi } from '@/lib/api/billing';
import { useState, useEffect } from 'react';
import { useToast } from '@/hooks/use-toast';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { Tag, AlertCircle } from 'lucide-react';
import { AuthModal } from '@/components/AuthModal';

export default function PricingPage() {
  const navigate = useNavigate();
  const { user } = useAuth();
  // Only fetch subscription if user is authenticated
  const { data: subscription } = useSubscription({ enabled: !!user });
  const { toast } = useToast();
  const [loadingPlan, setLoadingPlan] = useState<string | null>(null);
  const [promoCode, setPromoCode] = useState('');
  const [promoValidation, setPromoValidation] = useState<{
    valid: boolean;
    description?: string;
    error?: string;
  } | null>(null);
  const [validatingPromo, setValidatingPromo] = useState(false);
  const [authModalOpen, setAuthModalOpen] = useState(false);
  const [selectedPlanForAuth, setSelectedPlanForAuth] = useState<string | null>(null);

  // Handle plan selection after successful authentication
  useEffect(() => {
    const proceedWithSelectedPlan = async () => {
      if (user && selectedPlanForAuth) {
        const planId = selectedPlanForAuth;
        setSelectedPlanForAuth(null);
        
        if (planId === 'free') {
          navigate('/dashboard');
          return;
        }

        setLoadingPlan(planId);
        try {
          const successUrl = `${window.location.origin}/dashboard/billing?session_id={CHECKOUT_SESSION_ID}`;
          const cancelUrl = `${window.location.origin}/pricing`;
          
          const session = await billingApi.createCheckoutSession(
            planId as 'enterprise',
            successUrl,
            cancelUrl,
            promoValidation?.valid ? promoCode : undefined
          );

          // Redirect to Stripe Checkout
          window.location.href = session.checkout_url;
        } catch (error) {
          console.error('Failed to create checkout session:', error);
          toast({
            title: 'Error',
            description: 'Failed to start checkout process. Please try again.',
            variant: 'destructive'
          });
          setLoadingPlan(null);
        }
      }
    };

    proceedWithSelectedPlan();
  }, [user, selectedPlanForAuth, navigate, promoValidation, promoCode, toast]);

  const validatePromoCode = async () => {
    if (!promoCode.trim()) {
      setPromoValidation(null);
      return;
    }

    // Check if user is logged in first
    if (!user) {
      setPromoValidation({
        valid: false,
        error: 'Please sign in to apply promo codes'
      });
      return;
    }

    setValidatingPromo(true);
    try {
      const result = await billingApi.validatePromoCode(promoCode, 'enterprise');
      setPromoValidation(result);
    } catch (error) {
      setPromoValidation({
        valid: false,
        error: 'Failed to validate promo code'
      });
    } finally {
      setValidatingPromo(false);
    }
  };

  const handleSelectPlan = async (planId: string) => {
    if (!user) {
      // Store the selected plan and open auth modal
      setSelectedPlanForAuth(planId);
      setAuthModalOpen(true);
      return;
    }

    if (planId === 'free') {
      navigate('/dashboard');
      return;
    }

    setLoadingPlan(planId);
    try {
      const successUrl = `${window.location.origin}/dashboard/billing?session_id={CHECKOUT_SESSION_ID}`;
      const cancelUrl = `${window.location.origin}/pricing`;
      
      const session = await billingApi.createCheckoutSession(
        planId as 'enterprise',
        successUrl,
        cancelUrl,
        promoValidation?.valid ? promoCode : undefined
      );

      // Redirect to Stripe Checkout
      window.location.href = session.checkout_url;
    } catch (error) {
      console.error('Failed to create checkout session:', error);
      toast({
        title: 'Error',
        description: 'Failed to start checkout process. Please try again.',
        variant: 'destructive'
      });
      setLoadingPlan(null);
    }
  };

  const isCurrentPlan = (planId: string) => {
    if (!subscription) return false;
    return subscription.plan_type === planId;
  };

  return (
    <div className="min-h-screen bg-[#162856]">
      {/* Header */}
      <header className="border-b border-white/10">
        <div className="container mx-auto px-4 py-4 flex justify-between items-center">
          <h1 className="text-2xl font-bold cursor-pointer text-white" onClick={() => navigate('/')}>
            Omnara
          </h1>
          <div className="flex gap-4">
            {user ? (
              <Button onClick={() => navigate('/dashboard')} className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0">Dashboard</Button>
            ) : (
              <>
                <Button variant="ghost" onClick={() => setAuthModalOpen(true)} className="text-off-white hover:text-white hover:bg-white/10 transition-all duration-300">
                  Sign In
                </Button>
                <Button onClick={() => setAuthModalOpen(true)} className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0">Get Started</Button>
              </>
            )}
          </div>
        </div>
      </header>

      {/* Pricing Content */}
      <main className="container mx-auto px-4 py-16">
        <div className="text-center mb-12">
          <h2 className="text-4xl font-bold mb-4 text-white">Choose Your Plan</h2>
          <p className="text-xl text-off-white/80">
            Start free and scale as you grow
          </p>
        </div>

        <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto">
          {Object.values(PLANS).map((plan) => (
            <Card 
              key={plan.id} 
              className={`relative flex flex-col border border-white/20 bg-white/10 backdrop-blur-md hover:bg-white/15 transition-all duration-500 ${plan.id === 'pro' ? 'border-electric-accent/50 shadow-xl shadow-electric-blue/20' : ''}`}
            >
              {/* {plan.id === 'pro' && (
                <div className="absolute -top-4 left-1/2 -translate-x-1/2 bg-electric-accent text-midnight-blue px-3 py-1 rounded-full text-sm font-semibold border-0">
                  Most Popular
                </div>
              )} */}
              
              <CardHeader>
                <CardTitle className="text-2xl text-white">{plan.name}</CardTitle>
                <CardDescription>
                  {plan.id === 'pro' ? (
                    <div>
                      <div className="mb-2">
                        <span className="text-5xl font-bold text-white">{plan.priceDisplay}</span>
                        <span className="text-off-white/60 text-lg">/month</span>
                      </div>
                      <div className="inline-flex items-center px-3 py-1 rounded-full bg-electric-accent/20 border border-electric-accent/40">
                        <span className="text-sm font-semibold text-electric-accent">
                          Early Bird Special - Limited Time
                        </span>
                      </div>
                    </div>
                  ) : (
                    <>
                      <span className="text-3xl font-bold text-white">{plan.priceDisplay}</span>
                      {plan.price > 0 && <span className="text-off-white/60">/month</span>}
                    </>
                  )}
                </CardDescription>
              </CardHeader>

              <CardContent className="flex-1 flex flex-col">
                <div className="mb-6">
                  <p className="text-sm text-off-white/70 mb-2">Monthly Agents</p>
                  <p className="text-2xl font-semibold text-white">
                    {plan.limits.agents === -1 ? 'Unlimited' : plan.limits.agents}
                  </p>
                </div>

                <ul className="space-y-3 flex-1">
                  {plan.features.map((feature, index) => (
                    <li key={index} className="flex items-start gap-3">
                      <Check className="h-5 w-5 text-electric-accent shrink-0 mt-0.5" />
                      <span className="text-sm text-off-white/80">{feature}</span>
                    </li>
                  ))}
                </ul>
              </CardContent>

              <CardFooter>
                {plan.id === 'enterprise' ? (
                  <Button
                    className="w-full border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                    variant="outline"
                    onClick={() => window.open('https://cal.com/ishaan-sehgal-8kc22w/omnara-demo', '_blank')}
                  >
                    Schedule a Call
                  </Button>
                ) : (
                  <Button
                    className="w-full border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                    // className={`w-full ${
                    //   plan.id === 'pro' 
                    //     ? 'bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0' 
                    //     : 'border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300'
                    // }`}
                    variant="outline"
                    disabled={isCurrentPlan(plan.id) || loadingPlan === plan.id}
                    onClick={() => handleSelectPlan(plan.id)}
                  >
                    {loadingPlan === plan.id ? (
                      'Processing...'
                    ) : isCurrentPlan(plan.id) ? (
                      'Current Plan'
                    ) : plan.id === 'free' ? (
                      'Start Free'
                    ) : (
                      'Get Started'
                    )}
                  </Button>
                )}
              </CardFooter>
            </Card>
          ))}
        </div>

        {/* Promo Code Section */}
        <div className="max-w-md mx-auto mt-12">
          <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
            <CardContent className="p-6">
              <div className="flex items-center gap-2 mb-4">
                <Tag className="h-5 w-5 text-electric-accent" />
                <Label htmlFor="promo-code" className="text-base text-white">Have a promo code?</Label>
              </div>
              <div className="flex gap-2">
                <Input
                  id="promo-code"
                  type="text"
                  placeholder="Enter promo code"
                  value={promoCode}
                  onChange={(e) => {
                    setPromoCode(e.target.value);
                    setPromoValidation(null);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      validatePromoCode();
                    }
                  }}
                  className="bg-white/10 backdrop-blur-sm border-white/20 text-white placeholder:text-off-white/40 focus:ring-electric-accent focus:ring-offset-midnight-blue"
                />
                <Button
                  onClick={validatePromoCode}
                  disabled={validatingPromo || !promoCode.trim()}
                  variant="outline"
                  className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                >
                  {validatingPromo ? 'Validating...' : 'Apply'}
                </Button>
              </div>
              {promoValidation && (
                <Alert className={`mt-4 ${promoValidation.valid ? 'bg-green-500/20 border-green-400/30' : 'bg-red-500/20 border-red-400/30'}`}>
                  <AlertCircle className={`h-4 w-4 ${promoValidation.valid ? 'text-green-400' : 'text-red-400'}`} />
                  <AlertDescription className={promoValidation.valid ? 'text-green-200' : 'text-red-200'}>
                    {promoValidation.valid 
                      ? `✅ ${promoValidation.description}`
                      : promoValidation.error}
                  </AlertDescription>
                </Alert>
              )}
            </CardContent>
          </Card>
        </div>

        <div className="mt-16 text-center">
          <p className="text-sm text-off-white/60">
            Need more? <a href="mailto:contact@omnara.com" className="text-electric-accent hover:text-white transition-colors duration-300">Contact us</a> for custom plans
          </p>
        </div>
      </main>

      {/* Auth Modal */}
      <AuthModal
        isOpen={authModalOpen}
        onClose={() => {
          setAuthModalOpen(false);
          // If user closes modal without signing in, clear the selected plan
          if (!user) {
            setSelectedPlanForAuth(null);
          }
        }}
        onSuccess={() => {
          // Stay on pricing page after successful auth
          // The useEffect will handle the plan selection
        }}
        redirectTo={window.location.href}
      />
    </div>
  );
}