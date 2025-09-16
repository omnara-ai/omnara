import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Progress } from '@/components/ui/progress'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { useSubscription, useUsage } from '@/hooks/useSubscription'
import { billingApi } from '@/lib/api/billing'
import { PLANS } from '@/lib/stripe'
import { useToast } from '@/hooks/use-toast'
import { format } from 'date-fns'
import { 
  AlertCircle, 
  Users, 
  Calendar, 
  TrendingUp, 
  Phone, 
  Mail, 
  Bell,
  CreditCard,
  Smartphone,
  MessageSquare,
  CheckCircle
} from 'lucide-react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { apiClient } from '@/lib/dashboardApi'

interface NotificationSettings {
  push_notifications_enabled: boolean
  email_notifications_enabled: boolean
  sms_notifications_enabled: boolean
  phone_number: string | null
  notification_email: string
}

// E.164 phone number validation
const validateE164PhoneNumber = (phone: string): boolean => {
  if (!phone) return true // Empty is valid
  // E.164 format: + followed by 1-15 digits, first digit cannot be 0
  const e164Regex = /^\+[1-9]\d{1,14}$/
  return e164Regex.test(phone)
}

export default function SettingsPage() {
  const navigate = useNavigate()
  const { toast } = useToast()
  const queryClient = useQueryClient()
  
  // Billing data
  const { data: subscription, isLoading: subLoading, refetch: refetchSub } = useSubscription()
  const { data: usage, isLoading: usageLoading } = useUsage()
  
  // Notification settings
  const { data: notificationSettings, isLoading: notifLoading } = useQuery<NotificationSettings>({
    queryKey: ['notificationSettings'],
    queryFn: () => apiClient.getNotificationSettings()
  })
  
  // Form state
  const [phoneNumber, setPhoneNumber] = useState('')
  const [smsConsent, setSmsConsent] = useState(false)
  const [notificationEmail, setNotificationEmail] = useState('')
  const [pushEnabled, setPushEnabled] = useState(true)
  const [emailEnabled, setEmailEnabled] = useState(false)
  const [smsEnabled, setSmsEnabled] = useState(false)
  
  // Update form state when data loads
  useEffect(() => {
    if (notificationSettings) {
      setPhoneNumber(notificationSettings.phone_number || '')
      setNotificationEmail(notificationSettings.notification_email || '')
      setPushEnabled(notificationSettings.push_notifications_enabled)
      setEmailEnabled(notificationSettings.email_notifications_enabled)
      setSmsEnabled(notificationSettings.sms_notifications_enabled)
      setSmsConsent(!!notificationSettings.phone_number && notificationSettings.sms_notifications_enabled)
    }
  }, [notificationSettings])
  
  // Update notification settings mutation
  const updateSettingsMutation = useMutation({
    mutationFn: (settings: Partial<NotificationSettings>) => apiClient.updateNotificationSettings(settings),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['notificationSettings'] })
      toast({
        title: 'Settings updated',
        description: 'Your notification preferences have been saved.',
      })
    },
    onError: () => {
      toast({
        title: 'Error',
        description: 'Failed to update settings. Please try again.',
        variant: 'destructive'
      })
    }
  })
  
  
  const handleSaveNotifications = async () => {
    // Check if SMS is enabled but no phone number provided
    if (smsEnabled && !phoneNumber) {
      toast({
        title: 'Phone number required',
        description: 'Please enter a phone number to enable SMS notifications.',
        variant: 'destructive'
      })
      return
    }
    
    // Validate phone number format if provided
    if (phoneNumber && !validateE164PhoneNumber(phoneNumber)) {
      toast({
        title: 'Invalid phone number format',
        description: 'Phone number must be in E.164 format (e.g., +12125551234). Include + and country code.',
        variant: 'destructive'
      })
      return
    }
    
    // Check SMS consent if enabling SMS
    if (smsEnabled && phoneNumber && !smsConsent) {
      toast({
        title: 'SMS consent required',
        description: 'Please check the SMS consent box to enable SMS notifications.',
        variant: 'destructive'
      })
      return
    }
    
    await updateSettingsMutation.mutateAsync({
      push_notifications_enabled: pushEnabled,
      email_notifications_enabled: emailEnabled,
      sms_notifications_enabled: smsEnabled && smsConsent,
      phone_number: phoneNumber || null,
      notification_email: notificationEmail || null,
    })
  }
  
  const handleCancelSubscription = async () => {
    if (!window.confirm('Are you sure you want to cancel your subscription?')) {
      return
    }

    try {
      await billingApi.cancelSubscription()
      toast({
        title: 'Subscription cancelled',
        description: 'Your subscription will remain active until the end of the billing period.',
      })
      refetchSub()
    } catch (error) {
      toast({
        title: 'Error',
        description: 'Failed to cancel subscription. Please try again.',
        variant: 'destructive'
      })
    }
  }

  const handleUpgrade = () => {
    navigate('/pricing')
  }

  // Don't wait for all data to load - show what we have
  const isInitialLoading = subLoading && usageLoading && notifLoading

  const currentPlan = subscription ? PLANS[subscription.plan_type as keyof typeof PLANS] || PLANS.free : PLANS.free
  const usagePercentage = usage && subscription ? 
    (subscription.agent_limit === -1 ? 0 : (usage.total_agents / subscription.agent_limit) * 100) : 0

  if (isInitialLoading) {
    return (
      <div className="space-y-6 animate-fade-in max-w-4xl">
        <div>
          <h2 className="text-3xl font-bold tracking-tight text-white">Settings</h2>
          <p className="text-lg text-off-white/80 mt-2">
            Loading your settings...
          </p>
        </div>
        <div className="space-y-4">
          <div className="h-48 bg-white/10 rounded-lg"></div>
          <div className="h-48 bg-white/10 rounded-lg"></div>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-6 animate-fade-in max-w-4xl">
      <div>
        <h2 className="text-3xl font-bold tracking-tight text-white">Settings</h2>
        <p className="text-lg text-off-white/80 mt-2">
          Manage your account, billing, and notification preferences
        </p>
      </div>

      <Tabs defaultValue="notifications" className="space-y-6">
        <TabsList className="bg-white/10 border border-white/20">
          <TabsTrigger value="notifications" className="data-[state=active]:bg-white/20">
            <Bell className="w-4 h-4 mr-2" />
            Notifications
          </TabsTrigger>
          <TabsTrigger value="billing" className="data-[state=active]:bg-white/20">
            <CreditCard className="w-4 h-4 mr-2" />
            Billing
          </TabsTrigger>
        </TabsList>

        <TabsContent value="notifications" className="space-y-6">
          {/* Notification Preferences */}
          {notifLoading ? (
            <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
              <CardHeader>
                <CardTitle className="text-white">Loading notification settings...</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="space-y-4">
                  <div className="h-12 bg-white/10 rounded"></div>
                  <div className="h-12 bg-white/10 rounded"></div>
                  <div className="h-12 bg-white/10 rounded"></div>
                </div>
              </CardContent>
            </Card>
          ) : (
            <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
              <CardHeader>
                <CardTitle className="text-white">Notification Preferences</CardTitle>
                <CardDescription className="text-off-white/70">
                  Choose how you want to be notified when your agents need input
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-6">
              {/* Push Notifications */}
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <Label htmlFor="push" className="text-base text-white flex items-center gap-2">
                    <Smartphone className="w-4 h-4" />
                    Push Notifications
                  </Label>
                  <p className="text-sm text-off-white/60">
                    Receive notifications on your mobile device
                  </p>
                </div>
                <Switch
                  id="push"
                  checked={pushEnabled}
                  onCheckedChange={setPushEnabled}
                />
              </div>

              {/* Email Notifications */}
              <div className="space-y-4">
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label htmlFor="email" className="text-base text-white flex items-center gap-2">
                      <Mail className="w-4 h-4" />
                      Email Notifications
                    </Label>
                    <p className="text-sm text-off-white/60">
                      Receive notifications via email
                    </p>
                  </div>
                  <Switch
                    id="email"
                    checked={emailEnabled}
                    onCheckedChange={setEmailEnabled}
                  />
                </div>
                
                {emailEnabled && (
                  <div className="ml-6 space-y-2">
                    <Label htmlFor="notification-email" className="text-sm text-off-white/80">
                      Notification Email (optional)
                    </Label>
                    <Input
                      id="notification-email"
                      type="email"
                      placeholder="Leave blank to use account email"
                      value={notificationEmail}
                      onChange={(e) => setNotificationEmail(e.target.value)}
                      className="bg-white/5 border-white/20 text-white placeholder:text-off-white/40"
                    />
                  </div>
                )}
              </div>

              {/* SMS Notifications - Temporarily disabled */}
              {/* <div className="space-y-4">
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label htmlFor="sms" className="text-base text-white flex items-center gap-2">
                      <MessageSquare className="w-4 h-4" />
                      SMS Notifications
                    </Label>
                    <p className="text-sm text-off-white/60">
                      Receive notifications via SMS text message
                    </p>
                  </div>
                  <Switch
                    id="sms"
                    checked={smsEnabled}
                    onCheckedChange={setSmsEnabled}
                  />
                </div>
                
                {smsEnabled && (
                  <div className="ml-6 space-y-4">
                    <div className="space-y-2">
                      <Label htmlFor="phone" className="text-sm text-off-white/80">
                        Phone Number
                      </Label>
                      <Input
                        id="phone"
                        type="tel"
                        placeholder="+12125551234"
                        value={phoneNumber}
                        onChange={(e) => setPhoneNumber(e.target.value)}
                        className={`bg-white/5 border-white/20 text-white placeholder:text-off-white/40 ${
                          phoneNumber && !validateE164PhoneNumber(phoneNumber) ? 'border-red-500' : ''
                        }`}
                      />
                      <p className={`text-xs ${
                        phoneNumber && !validateE164PhoneNumber(phoneNumber) 
                          ? 'text-red-400' 
                          : 'text-off-white/50'
                      }`}>
                        Must be in E.164 format: + followed by country code and number (e.g., +12125551234)
                      </p>
                    </div>
                    
                    {/* SMS Consent */}
                    {/* <div className="bg-white/5 border border-white/10 rounded-lg p-4 space-y-3">
                      <div className="flex items-start space-x-3">
                        <Checkbox
                          id="sms-consent"
                          checked={smsConsent}
                          onCheckedChange={(checked) => setSmsConsent(checked as boolean)}
                          className="mt-1 border-white/40 data-[state=checked]:bg-electric-accent data-[state=checked]:border-electric-accent"
                        />
                        <div className="space-y-1">
                          <Label htmlFor="sms-consent" className="text-sm text-white font-normal cursor-pointer">
                            I consent to receive SMS notifications from Omnara
                          </Label>
                          <p className="text-xs text-off-white/60">
                            By checking this box, you agree to receive automated SMS messages from Omnara 
                            at the phone number provided. Message and data rates may apply.
                          </p>
                        </div>
                      </div>
                    </div>
                  </div>
                )}
              </div> */}

              {/* Save Button */}
              <div className="pt-4 flex justify-end">
                <Button
                  onClick={handleSaveNotifications}
                  disabled={updateSettingsMutation.isPending}
                  className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300"
                >
                  {updateSettingsMutation.isPending ? 'Saving...' : 'Save Notification Settings'}
                </Button>
              </div>
            </CardContent>
          </Card>
          )}
        </TabsContent>

        <TabsContent value="billing" className="space-y-6">
          {/* Current Plan */}
          <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
            <CardHeader>
              <CardTitle className="text-white">Current Plan</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="space-y-4">
                <div>
                  <h3 className="text-2xl font-bold text-white">{currentPlan.name}</h3>
                  <p className="text-off-white/70">
                    {currentPlan.priceDisplay}/month
                  </p>
                </div>

                {subscription?.cancel_at_period_end && (
                  <Alert className="bg-red-500/20 border-red-400/30">
                    <AlertCircle className="h-4 w-4 !text-white" />
                    <AlertDescription className="text-red-200">
                      Your subscription will be cancelled at the end of the current billing period
                    </AlertDescription>
                  </Alert>
                )}

                <div className="flex items-center justify-between">
                  <div className="flex gap-3">
                  {subscription?.plan_type !== 'enterprise' && (
                    <Button 
                      onClick={handleUpgrade}
                      className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0"
                    >
                      Upgrade Plan
                    </Button>
                  )}
                  {subscription?.plan_type !== 'free' && !subscription?.cancel_at_period_end && (
                    <Button 
                      variant="outline" 
                      onClick={handleCancelSubscription}
                      className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                    >
                      Cancel Subscription
                    </Button>
                  )}
                  </div>
                  {subscription?.current_period_end && (
                    <div className="flex items-center gap-2 text-sm text-off-white/60">
                      <Calendar className="h-4 w-4" />
                      Next billing: {format(new Date(subscription.current_period_end), 'MMM d, yyyy')}
                    </div>
                  )}
                </div>
              </div>
            </CardContent>
          </Card>

          {/* Usage Statistics */}
          <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-white">
                <Users className="h-5 w-5 text-electric-accent" />
                Monthly Agents
              </CardTitle>
              <CardDescription className="text-off-white/70">
                Total agents created this month
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="space-y-4">
                <div>
                  <div className="flex items-center justify-between mb-2">
                    <span className="text-2xl font-bold text-white">
                      {usage?.total_agents || 0}
                    </span>
                    <span className="text-off-white/70">
                      {subscription?.agent_limit === -1 ? 'Unlimited' : `of ${subscription?.agent_limit || 0} per month`}
                    </span>
                  </div>
                  {subscription?.agent_limit !== -1 && (
                    <Progress value={usagePercentage} className="h-2 bg-white/10" />
                  )}
                </div>
                
                {usagePercentage >= 80 && subscription?.agent_limit !== -1 && (
                  <Alert className="bg-orange-500/20 border-orange-400/30">
                    <TrendingUp className="h-4 w-4 text-orange-400" />
                    <AlertDescription className="text-orange-200">
                      You're approaching your monthly agent limit. Consider upgrading for unlimited agents.
                    </AlertDescription>
                  </Alert>
                )}
              </div>
            </CardContent>
          </Card>

          {/* Plan Features */}
          <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
            <CardHeader>
              <CardTitle className="text-white">Plan Features</CardTitle>
              <CardDescription className="text-off-white/70">
                Everything included in your {currentPlan.name} plan
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ul className="space-y-2">
                {currentPlan.features.map((feature, index) => (
                  <li key={index} className="flex items-center gap-2">
                    <CheckCircle className="h-4 w-4 text-electric-accent" />
                    <span className="text-sm text-off-white/80">{feature}</span>
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* Danger Zone - Delete Account */}
      <Card className="border border-red-500/30 bg-red-500/10 backdrop-blur-md mt-8">
        <CardHeader>
          <CardTitle className="text-white">Danger Zone</CardTitle>
          <CardDescription className="text-red-200/80">
            Permanent account deletion
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="space-y-4">
            <p className="text-sm text-off-white/80">
              Once you delete your account, there is no going back. All your data, agents, and instances will be permanently removed.
            </p>
            <Button
              variant="destructive"
              onClick={() => {
                if (window.confirm('Are you absolutely sure you want to delete your account? This action cannot be undone.')) {
                  // TODO: Implement account deletion
                  toast({
                    title: 'Account deletion',
                    description: 'Please contact support to delete your account.',
                  })
                }
              }}
              className="bg-red-600 hover:bg-red-700 text-white"
            >
              Delete Account
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}