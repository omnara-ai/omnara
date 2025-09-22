# Omnara Mobile App

*Backed by Y Combinator*

A React Native + Expo mobile application for the Omnara AI Agent Command Center, providing a native mobile experience for monitoring and interacting with AI agents.

## Architecture

### Technology Stack
- **Framework**: React Native 0.73+ with TypeScript
- **Development Platform**: Expo SDK 50 (managed workflow)
- **Navigation**: React Navigation v6 (native stack + bottom tabs)
- **State Management**: TanStack Query + Zustand
- **Authentication**: Supabase Auth SDK
- **UI Components**: Custom component library inspired by shadcn/ui
- **Styling**: NativeWind (Tailwind CSS for React Native)
- **Animation**: React Native Reanimated 3 + Lottie

### Project Structure
```
mobile/
├── src/
│   ├── app/                # Navigation and app entry
│   ├── components/         # Reusable components
│   │   ├── ui/            # Base UI components
│   │   ├── dashboard/     # Dashboard-specific
│   │   └── shared/        # Shared components
│   ├── contexts/          # React contexts
│   ├── screens/           # Screen components
│   ├── lib/               # Utilities and helpers
│   ├── hooks/             # Custom hooks
│   ├── services/          # API and external services
│   ├── store/             # Zustand stores
│   ├── types/             # TypeScript types
│   └── constants/         # App constants
└── assets/                # Images, fonts, etc.
```

## Getting Started

### Prerequisites
- Node.js 18+
- npm or yarn
- Expo Go app on your phone (for device testing)
- iOS Simulator (Mac) or Android Emulator (for simulator testing)

### Quick Start
```bash
# Navigate to mobile directory
cd mobile

# Install dependencies
npm install

# Start the development server
npx expo start

# Press 'i' for iOS simulator
# Press 'a' for Android emulator
# Scan QR code with Expo Go app for device testing
```

### Platform-Specific Commands
```bash
# Run on iOS Simulator
npm run ios

# Run on Android Emulator
npm run android

# Run on Web (experimental)
npm run web
```

### Environment Setup
1. Copy `.env.example` to `.env`
2. Add your Supabase credentials:
   ```
   EXPO_PUBLIC_SUPABASE_URL=your_supabase_url
   EXPO_PUBLIC_SUPABASE_ANON_KEY=your_supabase_anon_key
   EXPO_PUBLIC_API_BASE_URL=http://localhost:8000
   ```
3. Restart the app with `npx expo start -c` after updating environment variables

## Key Features

### Authentication
- Email/password login with Supabase
- Google OAuth support
- Biometric authentication for returning users
- Secure token storage with expo-secure-store

### UI Components
The app uses a custom component library inspired by shadcn/ui, adapted for React Native:
- **Button**: Variants (primary, secondary, outline, ghost, destructive) with haptic feedback
- **Card**: Glass morphism effects using expo-blur
- **Badge**: Status indicators with semantic colors
- **Input**: Floating label inputs with validation
- **Gradient**: Linear gradients for backgrounds

### Navigation Structure
- **Bottom Tabs**:
  - Command Center (Home)
  - Instances
  - API Keys
  - Profile
- **Stack Screens**:
  - Instance Detail
  - Authentication flows

### Design System
- **Colors**: Consistent with web app (Electric Blue, Midnight Blue, etc.)
- **Typography**: System fonts with defined size scale
- **Spacing**: 4px base unit system
- **Shadows**: Platform-specific elevation
- **Animations**: Spring-based transitions

## Development

### Running Tests
```bash
npm test
```

### Building for Production
```bash
# iOS
eas build --platform ios

# Android
eas build --platform android
```

### Code Quality
- TypeScript strict mode enabled
- ESLint configuration extends Expo defaults
- Prettier for code formatting

## Mobile-Specific Features
- Haptic feedback on interactions
- Pull-to-refresh on lists
- Swipe gestures for navigation
- Background fetch for notifications
- Deep linking support (omnara://)
- Offline data caching

## Performance Considerations
- Lazy loading of screens
- Virtualized lists for large datasets
- Image optimization
- Memoization of expensive computations
- Minimal re-renders with proper React patterns

## Security
- Secure storage for authentication tokens
- Certificate pinning for API calls
- Biometric authentication support
- Data encryption at rest

## Troubleshooting

### Common Issues
1. **Metro bundler issues**: Clear cache with `npx expo start -c`
2. **iOS build failures**: Ensure XCode is updated
3. **Android build issues**: Check Android SDK version
4. **Navigation type errors**: Run `npm run typecheck`

### Debug Mode
- Shake device or press Cmd+D (iOS) / Cmd+M (Android)
- Use React Native Debugger for advanced debugging
- Check Expo Go logs for runtime errors