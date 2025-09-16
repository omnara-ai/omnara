import { UserAgentConfig } from '@/components/dashboard'

export default function UserAgents() {
  return (
    <div className="space-y-6 animate-fade-in">
      <div>
        <h1 className="text-3xl font-bold tracking-tight text-white">
          Agent Management
        </h1>
        <p className="text-lg text-off-white/80 mt-2">
          Configure your AI agents and webhook integrations
        </p>
      </div>
      
      <UserAgentConfig />
    </div>
  )
}