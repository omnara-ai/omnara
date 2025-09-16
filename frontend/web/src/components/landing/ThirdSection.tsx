import { asset } from '@/lib/assets';

const ThirdSection = () => {
  return (
    <section className="relative min-h-screen bg-black overflow-hidden">
      <style>{`
        .retro-title-third {
          font-family: 'Press Start 2P', cursive;
          font-size: 2.2rem;
          letter-spacing: 0.05em;
          position: relative;
          display: inline-block;
          color: hsl(var(--omnara-cream-text));
          text-transform: uppercase;
          filter: drop-shadow(0 2px 8px rgba(0,0,0,0.8));
        }
        
        .retro-title-third::before {
          content: "ALWAYS IN THE LOOP";
          position: absolute;
          left: 0;
          top: 0;
          color: #2a2a2a;
          z-index: -1;
          transform: translate(4px, 4px);
        }
        
        .retro-title-third::after {
          content: "ALWAYS IN THE LOOP";
          position: absolute;
          left: 0;
          top: 0;
          color: #1a1a1a;
          z-index: -2;
          transform: translate(8px, 8px);
        }
        
        @media (max-width: 768px) {
          .retro-title-third {
            font-size: 1.25rem;
            letter-spacing: 0.03em;
          }
        }
        
        @media (min-width: 1024px) {
          .retro-title-third {
            font-size: 2.8rem;
            letter-spacing: 0.08em;
          }
        }
        
        @keyframes fadeInUp {
          from {
            opacity: 0;
            transform: translateY(20px);
          }
          to {
            opacity: 1;
            transform: translateY(0);
          }
        }
        
        .bullet-item {
          animation: fadeInUp 0.6s ease-out forwards;
          opacity: 0;
          transition: transform 0.2s ease;
        }
        
        .bullet-item:hover {
          transform: translateX(4px);
        }
        
        .bullet-item:hover span:first-child {
          animation: bounce 0.5s ease;
        }
        
        @keyframes bounce {
          0%, 100% { transform: scale(1); }
          50% { transform: scale(1.2); }
        }
        
        /* Hide video controls */
        video::-webkit-media-controls {
          display: none !important;
        }
        
        video::-webkit-media-controls-enclosure {
          display: none !important;
        }
        
        video::-webkit-media-controls-panel {
          display: none !important;
        }
        
        video::-webkit-media-controls-play-button {
          display: none !important;
        }
        
        video {
          outline: none;
          border: none;
        }
        
        .bullet-item:nth-child(1) { animation-delay: 0.1s; }
        .bullet-item:nth-child(2) { animation-delay: 0.2s; }
        .bullet-item:nth-child(3) { animation-delay: 0.3s; }
        
        .retro-headline {
          font-family: 'Press Start 2P', cursive;
          font-size: 1rem;
          line-height: 1.6;
          letter-spacing: 0.03em;
          color: hsl(var(--omnara-cream-text));
          text-shadow: 0 2px 8px rgba(0,0,0,0.8);
        }
        
        @media (max-width: 640px) {
          .retro-headline {
            font-size: 0.9375rem;
            line-height: 1.4;
          }
        }
        
        @media (min-width: 768px) {
          .retro-headline {
            font-size: 1.5625rem;
            line-height: 1.8;
          }
        }
        
        @media (min-width: 1024px) {
          .retro-headline {
            font-size: 1.875rem;
            line-height: 1.8;
          }
        }
      `}</style>
      
      {/* Background Image */}
      <picture className="absolute inset-0 w-full h-full">
        <source type="image/avif" srcSet={asset('/uploads/backgrounds/phone_bg_3/phone_bg_3.avif')} />
        <source type="image/webp" srcSet={asset('/uploads/backgrounds/phone_bg_3/phone_bg_3.webp')} />
        <img
          src={asset('/uploads/backgrounds/phone_bg_3/phone_bg_3.jpg')}
          alt="Third section background"
          className="absolute inset-0 w-full h-full object-cover"
          decoding="async"
        />
      </picture>
      
      {/* Dark overlay for overall tinting */}
      <div 
        className="absolute inset-0"
        style={{
          backgroundColor: 'rgba(20, 20, 40, 0.5)', // Dark navy tint
        }}
      />
      
      {/* Top gradient fade - blends from previous section */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'linear-gradient(to bottom, #000000 0%, rgba(0, 0, 0, 0.7) 5%, rgba(0, 0, 0, 0.4) 15%, rgba(0, 0, 0, 0.2) 25%, transparent 35%)'
        }}
      />
      
      {/* Additional subtle vignette for depth */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'radial-gradient(ellipse at center, transparent 0%, rgba(0, 0, 0, 0.3) 60%, rgba(0, 0, 0, 0.5) 100%)'
        }}
      />
      
      {/* Bottom gradient fade to next section */}
      <div 
        className="absolute inset-0"
        style={{
          background: 'linear-gradient(to bottom, transparent 75%, rgba(0, 0, 0, 0.2) 85%, rgba(0, 0, 0, 0.5) 92%, rgba(0, 0, 0, 0.8) 98%)'
        }}
      />
      
      {/* Horizontal Divider */}
      <div className="absolute top-0 left-0 right-0 h-px bg-gradient-to-r from-transparent via-white/10 to-transparent"></div>
      
      {/* Section Title */}
      <div className="relative z-10 pt-20 md:pt-24 lg:pt-28 text-center">
        <h2 className="retro-title-third">
          Always in the Loop
        </h2>
      </div>
      
      {/* Split Layout Container */}
      <div className="relative z-10 mt-12 md:mt-16 lg:mt-20 px-6 pb-16 md:pb-20 lg:pb-24">
        <div className="max-w-7xl mx-auto">
          <div className="flex flex-col lg:grid lg:grid-cols-[45fr_55fr] items-center gap-8 md:gap-12 lg:gap-6">
            {/* Left Side - Phone Video */}
            <div className="flex justify-center lg:justify-end mt-6 md:mt-8 lg:mt-12 -mr-0 lg:-mr-32">
              <video
                src={asset('/uploads/animations/phone_notifications_H.265.mp4')}
                autoPlay
                loop
                muted
                playsInline
                controls={false}
                disablePictureInPicture
                controlsList="nodownload nofullscreen noremoteplayback"
                className="w-full max-w-[350px] sm:max-w-[400px] md:max-w-[500px] lg:max-w-none lg:w-full h-auto object-contain scale-[1.1] sm:scale-[1.2] md:scale-[1.4] lg:scale-[1.6]"
                style={{
                  filter: 'drop-shadow(0 20px 40px rgba(0,0,0,0.5)) drop-shadow(0 0 20px rgba(139,92,246,0.3))',
                  WebkitTapHighlightColor: 'transparent',
                  userSelect: 'none',
                }}
              >
                Your browser does not support the video tag.
              </video>
            </div>
            
            {/* Right Side - Content */}
            <div className="text-center lg:text-left pl-4 sm:pl-6 md:pl-12 lg:pl-12 pr-4 sm:pr-6 md:pr-12 lg:pr-16 mt-0 lg:-mt-0 max-w-[600px] lg:max-w-none mx-auto lg:mx-0">
              <div className="mb-6 sm:mb-8 md:mb-10 text-center lg:text-left">
                <p className="retro-headline">
                  Never in the Dark.
                </p>
              </div>
              
              <div className="space-y-4">
                <div className="flex items-center gap-3 bullet-item justify-center lg:justify-start">
                  <span className="text-2xl flex-shrink-0">🔔</span>
                  <p className="text-gray-300 text-lg sm:text-xl" style={{ textShadow: '0 2px 4px rgba(0,0,0,0.7)' }}>
                    Get notified the moment agents need help
                  </p>
                </div>
                
                <div className="flex items-center gap-3 bullet-item justify-center lg:justify-start">
                  <span className="text-2xl flex-shrink-0">🖐️</span>
                  <p className="text-gray-300 text-lg sm:text-xl" style={{ textShadow: '0 2px 4px rgba(0,0,0,0.7)' }}>
                    Tap to approve, guide, or take over
                  </p>
                </div>
                
                <div className="flex items-center gap-3 bullet-item justify-center lg:justify-start">
                  <span className="text-2xl flex-shrink-0">🛡️</span>
                  <p className="text-gray-300 text-lg sm:text-xl" style={{ textShadow: '0 2px 4px rgba(0,0,0,0.7)' }}>
                    Fail-safe: no silent crashes or missed steps
                  </p>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
};

export default ThirdSection;
