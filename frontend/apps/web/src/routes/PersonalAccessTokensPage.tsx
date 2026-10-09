import { PersonalAccessTokensSection } from '@/components/api-tokens/PersonalAccessTokensSection'

export function PersonalAccessTokensPage() {
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PersonalAccessTokensSection />
    </div>
  )
}
