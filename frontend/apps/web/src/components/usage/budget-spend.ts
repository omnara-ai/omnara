const usdFractionDigits = 15

function usdUnits(decimal: string) {
  const [whole = '0', fraction = ''] = decimal.split('.')
  return BigInt(whole + fraction.padEnd(usdFractionDigits, '0').slice(0, usdFractionDigits))
}

export function spendReachesBudget(spent: string, budget: string) {
  return usdUnits(spent) >= usdUnits(budget)
}
