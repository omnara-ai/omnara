// Environment configuration
// Replace with your actual RevenueCat API key
export const REVENUECAT_API_KEY = process.env.EXPO_PUBLIC_REVENUECAT_API_KEY || '';

console.log('[ENV] RevenueCat API Key loaded:', REVENUECAT_API_KEY ? `${REVENUECAT_API_KEY.substring(0, 10)}...` : 'NOT LOADED');

// Note: In Expo, environment variables must be prefixed with EXPO_PUBLIC_
// Set this in your .env file as: EXPO_PUBLIC_REVENUECAT_API_KEY=your_key_here