export function InsufficientCreditsMessage({ billingHref }: { billingHref?: string }) {
  return (
    <>
      Insufficient Omnara credits.{' '}
      {billingHref ? (
        <a className="font-medium underline underline-offset-2" href={billingHref}>
          Add credits
        </a>
      ) : (
        'Ask an organization admin to add credits.'
      )}
    </>
  )
}
