# OTA Updates

## Setup
- expo-updates is installed and configured
- Updates check automatically on app launch
- If available, downloads and applies immediately
- Silent fail if error occurs

## Development Mode vs Development Build
- Development mode (`expo start`): OTA updates disabled
- Development build: OTA updates enabled

## Usage

### Build
```bash
# Development
eas build --profile development --platform ios

# Production
eas build --profile production --platform ios
```

### Publish Update
```bash
# Development channel
eas update --branch development --message "message"

# Production channel
eas update --branch production --message "message"
```

## Update Channels
- development
- preview
- production

## Updateable
- JavaScript code
- Images/assets

## Not Updateable
- Native code
- Native dependencies
- Permissions