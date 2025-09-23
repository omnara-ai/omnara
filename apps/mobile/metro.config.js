const path = require('path');
const {
  getSentryExpoConfig
} = require("@sentry/react-native/metro");

const config = getSentryExpoConfig(__dirname);

// Add support for additional file extensions if needed
config.resolver.sourceExts.push('cjs');

// Enable monorepo-style shared source consumption
// In this repo, packages is a sibling of the mobile app under frontend/
config.watchFolders = [path.resolve(__dirname, '../packages')];
config.resolver.nodeModulesPaths = [
  path.resolve(__dirname, 'node_modules'),
  path.resolve(__dirname, '../../node_modules'),
];

module.exports = config;