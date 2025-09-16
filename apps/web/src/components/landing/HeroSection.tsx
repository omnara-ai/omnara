import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { AuthModal } from '../AuthModal';
import { useAuth } from '@/lib/auth/AuthContext';
import { Copy } from 'lucide-react';
import { toast } from 'sonner';

const OmnaraLogo = () => {
  const [isAuthModalOpen, setIsAuthModalOpen] = useState(false);
  const navigate = useNavigate();
  const { user } = useAuth();

  const handleLaunchWebApp = () => {
    if (user) {
      navigate('/dashboard');
    } else {
      setIsAuthModalOpen(true);
    }
  };

  const handleDownloadApp = () => {
    window.open('https://apps.apple.com/us/app/omnara-ai-command-center/id6748426727', '_blank');
  };

  return (
    <div className="relative p-6 sm:p-10 md:p-14 pt-12 sm:pt-16 md:pt-20 animate-fade-in">
      
      
      <div className="text-center">
        <h1 className="retro-text mb-8">OMNARA</h1>
        
        <div className="space-y-4 mb-8">
          <h2 className="text-xl sm:text-2xl md:text-3xl font-semibold text-omnara-cream-text mb-3 text-shadow-lg">
            Launch & Control Claude Code from Anywhere
          </h2>
          <p className="text-base sm:text-lg md:text-xl text-omnara-cream-text/70 font-light max-w-2xl mx-auto leading-relaxed px-4 sm:px-0 text-shadow-md">
            Stop being chained to your desk. The real-time command center to monitor, debug, and guide your agent—right from your phone.
          </p>
        </div>
        
        <div className="flex flex-col sm:flex-row gap-4 justify-center items-center">
          <button 
            onClick={handleLaunchWebApp}
            className="h-14 sm:h-16 md:h-[72px] px-6 bg-amber-500/90 hover:bg-amber-500 text-black font-semibold rounded-lg transition-all duration-200 hover:scale-105 hover:shadow-lg hover:shadow-amber-500/50 border-2 border-amber-400/50 font-mono text-base w-auto flex items-center justify-center"
          >
            {user ? 'Go to Web App' : 'Get Started for Free'}
          </button>
          <a
            href="https://apps.apple.com/us/app/omnara-ai-command-center/id6748426727"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-block hover:scale-105 transition-transform duration-300"
            aria-label="Download on the App Store"
          >
            <img 
              src="/app-store-badge-black.svg" 
              alt="Download on the App Store"
              className="h-14 sm:h-16 md:h-[72px] w-auto hover:opacity-90 transition-opacity duration-300"
            />
          </a>
          <a
            href="https://play.google.com/store/apps/details?id=com.omnara.app"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-block hover:scale-105 transition-transform duration-300"
            aria-label="Get it on Google Play"
          >
            <img 
              src="/GetItOnGooglePlay_Badge_Web_color_English.png" 
              alt="Get it on Google Play"
              className="h-14 sm:h-16 md:h-[72px] w-auto hover:opacity-90 transition-opacity duration-300"
            />
          </a>
        </div>
        
        {/* Installation Command */}
        <div className="mt-6 flex flex-col items-center space-y-2">
          <span className="text-omnara-cream-text/60 text-sm text-shadow-md">Or get started with:</span>
          <button
            onClick={() => {
              navigator.clipboard.writeText('pip install omnara && omnara')
              toast.success('Command copied to clipboard!')
            }}
            className="group flex items-center space-x-3 px-6 py-3 bg-white/5 backdrop-blur-sm border border-white/10 rounded-lg hover:border-amber-500/30 hover:bg-amber-500/10 transition-all duration-200"
          >
            <code className="text-base md:text-lg font-mono text-omnara-cream-text text-shadow-sm">pip install omnara && omnara</code>
            <Copy className="w-4 h-4 text-omnara-cream-text/50 group-hover:text-amber-400 transition-colors" />
          </button>
        </div>
      </div>
      
      <AuthModal
        isOpen={isAuthModalOpen}
        onClose={() => setIsAuthModalOpen(false)}
        onSuccess={() => {
          setIsAuthModalOpen(false);
          navigate('/dashboard');
        }}
        initialMode="signin"
      />
    </div>
  );
};

export default OmnaraLogo;
