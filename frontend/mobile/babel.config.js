module.exports = function(api) {
  api.cache(true);
  return {
    presets: ['babel-preset-expo'],
    plugins: [
      [
        'module-resolver',
        {
          root: ['./'],
          alias: {
            '@': './src',
            '@/components': './src/components',
            '@/lib': './src/lib',
            '@/hooks': './src/hooks',
            '@/services': './src/services',
            '@/store': './src/store',
            '@/types': './src/types',
            '@/constants': './src/constants',
            '@/contexts': './src/contexts',
            '@/screens': './src/screens',
            '@omnara/shared': '../packages/shared/src',
          },
        },
      ],
      'react-native-reanimated/plugin',
    ],
  };
};
