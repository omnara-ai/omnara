import { useState } from 'react';
import { useAuth } from '@/lib/auth/AuthContext';
import { Link } from 'react-router-dom';
import { Copy } from 'lucide-react';
import { toast } from 'sonner';
import { AuthModal } from '../AuthModal';
import { asset } from '@/lib/assets';

const FinalCTASection = () => {
  const { user } = useAuth();
  const [authModalOpen, setAuthModalOpen] = useState(false);
  
  return (
    <section className="relative min-h-[80vh] bg-black overflow-hidden flex items-center">
      <style>{`
        .retro-cta-title {
          font-family: 'Press Start 2P', cursive;
          font-size: 1.75rem;
          letter-spacing: 0.08em;
          position: relative;
          display: inline-block;
          color: hsl(var(--omnara-cream-text));
          text-transform: uppercase;
          filter: drop-shadow(0 2px 8px rgba(0,0,0,0.8));
        }
        
        .retro-cta-title::before {
          content: 'Ready to Take Command?';
          position: absolute;
          left: 0;
          top: 0;
          color: #2a2a2a;
          z-index: -1;
          transform: translate(4px, 4px);
        }
        
        .retro-cta-title::after {
          content: 'Ready to Take Command?';
          position: absolute;
          left: 0;
          top: 0;
          color: #1a1a1a;
          z-index: -2;
          transform: translate(8px, 8px);
        }
        
        @media (max-width: 640px) {
          .retro-cta-title {
            font-size: 1.25rem;
            letter-spacing: 0.05em;
          }
          .retro-cta-title::before {
            transform: translate(3px, 3px);
          }
          .retro-cta-title::after {
            transform: translate(6px, 6px);
          }
        }
        
        @media (min-width: 768px) {
          .retro-cta-title {
            font-size: 2.25rem;
            letter-spacing: 0.1em;
          }
          .retro-cta-title::before {
            transform: translate(4px, 4px);
          }
          .retro-cta-title::after {
            transform: translate(8px, 8px);
          }
        }
        
        @media (min-width: 1024px) {
          .retro-cta-title {
            font-size: 3rem;
          }
        }
      `}</style>
      
      {/* Background Image */}
      <picture className="absolute inset-0 w-full h-full">
        <source type="image/avif" srcSet={asset('/uploads/backgrounds/phone_bg_1/phone_bg_1.avif')} />
        <source type="image/webp" srcSet={asset('/uploads/backgrounds/phone_bg_1/phone_bg_1.webp')} />
        <img
          src={asset('/uploads/backgrounds/phone_bg_1/phone_bg_1.jpg')}
          alt="Final CTA background"
          className="absolute inset-0 w-full h-full object-cover"
          decoding="async"
        />
      </picture>
      
      {/* Dark overlay for overall tinting */}
      <div 
        className="absolute inset-0"
        style={{
          backgroundColor: 'rgba(20, 20, 40, 0.3)', // Dark navy tint - reduced for more visibility
        }}
      />
      
      {/* Gradient overlay for text readability */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'radial-gradient(ellipse at center, rgba(0, 0, 0, 0.3) 0%, rgba(0, 0, 0, 0.5) 100%)'
        }}
      />
      
      {/* Top gradient fade from previous section */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'linear-gradient(to bottom, #000000 0%, rgba(0, 0, 0, 0.7) 5%, rgba(0, 0, 0, 0.4) 15%, rgba(0, 0, 0, 0.2) 25%, transparent 35%)'
        }}
      />
      
      {/* Content Container */}
      <div className="relative z-10 w-full px-6 py-16 md:py-20 lg:py-24">
        <div className="max-w-4xl mx-auto text-center">
          {/* Headline */}
          <h2 className="retro-cta-title mb-12 md:mb-16">
            Ready to Take Command?
          </h2>
          
          {/* CTA Buttons */}
          <div className="flex flex-col sm:flex-row gap-6 sm:gap-8 justify-center items-center">
            {user ? (
              <Link
                to="/dashboard"
                className="h-14 sm:h-16 md:h-[72px] px-6 bg-amber-500/90 hover:bg-amber-500 text-black font-semibold rounded-lg transition-all duration-200 hover:scale-105 hover:shadow-lg hover:shadow-amber-500/50 border-2 border-amber-400/50 font-mono text-base w-full sm:w-auto flex items-center justify-center"
              >
                Go to Web App
              </Link>
            ) : (
              <button 
                className="h-14 sm:h-16 md:h-[72px] px-6 bg-amber-500/90 hover:bg-amber-500 text-black font-semibold rounded-lg transition-all duration-200 hover:scale-105 hover:shadow-lg hover:shadow-amber-500/50 border-2 border-amber-400/50 font-mono text-base w-full sm:w-auto flex items-center justify-center"
                onClick={() => setAuthModalOpen(true)}
              >
                Get Started for Free
              </button>
            )}
            
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
                className="h-16 md:h-[72px] w-auto hover:opacity-90 transition-opacity duration-300"
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
                className="h-16 md:h-[72px] w-auto hover:opacity-90 transition-opacity duration-300"
              />
            </a>
          </div>
          
          {/* Installation Command */}
          <div className="mt-16 flex flex-col items-center space-y-4">
            <span className="text-off-white/80 text-sm">Or get started with:</span>
            <button
              onClick={() => {
                navigator.clipboard.writeText('pip install omnara && omnara')
                toast.success('Command copied to clipboard!')
              }}
              className="group flex items-center space-x-4 px-10 py-6 bg-black/50 backdrop-blur-sm border border-white/20 rounded-lg hover:border-white/40 hover:bg-black/60 transition-all duration-200"
            >
              <code className="text-xl md:text-2xl font-mono text-white font-medium">pip install omnara && omnara</code>
              <Copy className="w-6 h-6 text-white/70 group-hover:text-white transition-colors" />
            </button>
            <div className="text-white/50 text-sm mt-2">
              with uv: 
              <button
                onClick={() => {
                  navigator.clipboard.writeText('uv pip install omnara && uv run omnara')
                  toast.success('Command copied to clipboard!')
                }}
                className="ml-2 text-white/60 hover:text-omnara-gold transition-colors"
              >
                <code className="font-mono">uv pip install omnara && uv run omnara</code>
                <Copy className="w-4 h-4 inline ml-1 mb-0.5" />
              </button>
            </div>
          </div>
          
          {/* Contact Email */}
          <div className="mt-12 text-center">
            <p className="text-off-white/80 text-sm">
              For feedback and inquiries: <a href="mailto:ishaan@omnara.com" className="text-omnara-gold hover:text-omnara-gold-light transition-colors font-medium">ishaan@omnara.com</a>
            </p>
          </div>
          
          {/* Legal Links */}
          <div className="mt-8 text-center">
            <div className="flex justify-center items-center space-x-4 text-xs">
              <Link to="/privacy" className="text-off-white/60 hover:text-omnara-gold transition-colors">
                Privacy Policy
              </Link>
              <span className="text-off-white/40">•</span>
              <Link to="/terms" className="text-off-white/60 hover:text-omnara-gold transition-colors">
                Terms of Use
              </Link>
            </div>
          </div>
        </div>
      </div>
      
      <AuthModal 
        isOpen={authModalOpen} 
        onClose={() => setAuthModalOpen(false)} 
      />
    </section>
  );
};

export default FinalCTASection;
