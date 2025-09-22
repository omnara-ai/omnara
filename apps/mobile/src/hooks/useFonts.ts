import { useFonts as useExpoFonts } from 'expo-font';
import {
  Inter_300Light,
  Inter_300Light_Italic,
  Inter_400Regular,
  Inter_400Regular_Italic,
  Inter_500Medium,
  Inter_600SemiBold,
  Inter_700Bold,
  Inter_800ExtraBold,
  Inter_900Black,
} from '@expo-google-fonts/inter';

export const useFonts = () => {
  const [fontsLoaded, fontError] = useExpoFonts({
    'Inter-Light': Inter_300Light,
    'Inter-Light-Italic': Inter_300Light_Italic,
    'Inter-Regular': Inter_400Regular,
    'Inter-Regular-Italic': Inter_400Regular_Italic,
    'Inter-Medium': Inter_500Medium,
    'Inter-SemiBold': Inter_600SemiBold,
    'Inter-Bold': Inter_700Bold,
    'Inter-ExtraBold': Inter_800ExtraBold,
    'Inter-Black': Inter_900Black,
  });

  return { fontsLoaded, fontError };
};